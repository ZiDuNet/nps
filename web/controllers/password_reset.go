package controllers

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/version"
	"ehang.io/nps/web/audit"
	"github.com/astaxie/beego"
)

// Recovery codes never share a namespace with registration codes. Bounded
// stores limit memory use; five wrong guesses consume an issued code.
var passwordResetCodes = newRegistrationEmailCodeStore(maxRegistrationEmailCodes)
var passwordResetAttempts = newRegistrationRateLimiter(maxRegistrationRateRecords)

func sendPasswordResetEmail(recipient, code string, config registrationSMTPConfig) error {
	body := fmt.Sprintf("您的 NPS 密码找回验证码是：%s\r\n\r\n验证码 10 分钟内有效，仅用于重置账号密码，请勿泄露。若非本人操作，请忽略本邮件。\r\n", code)
	return sendAccountEmail(recipient, "NPS 密码找回验证码", body, config)
}

type passwordResetDelivery struct {
	email, code string
	config      registrationSMTPConfig
	eligible    bool
	failure     audit.Event
}

var passwordResetMailQueue = make(chan passwordResetDelivery, 64)
var passwordResetMailWorkers sync.Once
var queuePasswordResetDelivery = enqueuePasswordResetDelivery

func enqueuePasswordResetDelivery(job passwordResetDelivery) bool {
	passwordResetMailWorkers.Do(func() {
		for i := 0; i < 2; i++ {
			go func() {
				for next := range passwordResetMailQueue {
					if !next.eligible {
						continue
					}
					if err := sendPasswordResetEmail(next.email, next.code, next.config); err != nil {
						if err := audit.Append(next.failure); err != nil {
							beego.Error("写入找回邮件失败审计失败:", err)
						}
					}
				}
			}()
		}
	})
	select {
	case passwordResetMailQueue <- job:
		return true
	default:
		return false
	}
}

const passwordResetTTL = 10 * time.Minute
const passwordResetCooldown = time.Minute

func passwordResetBinding(user *file.User) (int, string, string, string) {
	if user == nil {
		return 0, "", "", "unbound"
	}
	user.RLock()
	id, email, password, name := user.Id, user.Email, user.Password, user.UserName
	user.RUnlock()
	digest := sha256.Sum256([]byte(strconv.Itoa(id) + "\x00" + email + "\x00" + password))
	return id, password, name, fmt.Sprintf("%x", digest)
}

func (self *LoginController) recoveryError(status int, code, zh, en string) {
	self.Ctx.Output.SetStatus(status)
	self.Data["json"] = authJSONError(code, zh, en)
	self.ServeJSON()
}

func (self *LoginController) recoveryPost() bool {
	setNoStore(self.Ctx)
	if !self.requirePost() {
		return false
	}
	if !beego.AppConfig.DefaultBool("allow_user_register", false) {
		self.recoveryError(http.StatusForbidden, "password_reset_disabled", "当前未开放注册，密码找回不可用，请联系管理员。", "Password recovery is unavailable while registration is disabled. Contact the administrator.")
		return false
	}
	request := self.Ctx.Request
	request.Body = http.MaxBytesReader(self.Ctx.ResponseWriter, request.Body, 4096)
	if request.ContentLength > 4096 || request.ParseForm() != nil {
		self.recoveryError(http.StatusRequestEntityTooLarge, "request_too_large", "请求过大或格式无效。", "Request too large or malformed.")
		return false
	}
	// The per-browser nonce also prevents cross-site mail requests. Do not
	// accept a recovery request without first loading the recovery page.
	challengeExpires, challengeSignature := loginChallengeFromForm(&self.Controller)
	if !crypt.VerifyLoginChallenge(strings.TrimSpace(self.GetString("nonce")), challengeExpires, challengeSignature, time.Now()) {
		self.recoveryError(http.StatusBadRequest, "password_reset_expired", "找回请求已过期，请刷新页面。", "Recovery request expired. Refresh the page.")
		return false
	}
	email := strings.ToLower(strings.TrimSpace(self.GetString("email")))
	policy := registrationPolicy{window: time.Minute, attempts: 10}
	if !passwordResetAttempts.reserveAttempt(requestVisitorIP(self.Ctx), email, time.Now(), policy) {
		self.recoveryError(http.StatusTooManyRequests, "password_reset_rate_limited", "找回请求过于频繁，请稍后重试。", "Too many recovery attempts. Try again later.")
		return false
	}
	if normalized, err := file.NormalizeUserEmail(email); err != nil || email == "" || normalized != email {
		self.recoveryError(http.StatusBadRequest, "invalid_email", "请填写账号绑定的有效邮箱地址。", "Enter the valid email address linked to your account.")
		return false
	}
	if registrationCaptchaEnabled() || beego.AppConfig.DefaultBool("open_captcha", false) {
		if cpt == nil || !cpt.VerifyReq(request) {
			self.recoveryError(http.StatusBadRequest, "captcha_invalid", "图片验证码错误，请刷新后重试。", "The image verification code is incorrect. Refresh it and try again.")
			return false
		}
	}
	return true
}

// Recover is public, but only independent user accounts with a bound email
// can be recovered. The administrator and client VKEYs are never modified.
func (self *LoginController) Recover() {
	if self.Ctx.Request.Method != http.MethodGet {
		self.ResetPassword()
		return
	}
	setNoStore(self.Ctx)
	base := beego.AppConfig.String("web_base_url")
	if !beego.AppConfig.DefaultBool("allow_user_register", false) {
		self.Redirect(base+"/login/index", http.StatusFound)
		return
	}
	self.Data["web_base_url"], self.Data["version"] = base, version.VERSION
	self.Data["public_key"], _ = crypt.GetRSAPublicKeyPEM()
	self.Data["captcha_open"] = registrationCaptchaEnabled() || beego.AppConfig.DefaultBool("open_captcha", false)
	_, err := configuredRegistrationSMTP()
	self.Data["email_ready"] = err == nil
	self.TplName = "login/recover.html"
}

func (self *LoginController) ResetEmailCode() {
	defer func() {
		self.auditAuthResponse("auth.password_reset.email_code", self.Ctx.Request.PostForm.Get("email"), "")
	}()
	if !self.recoveryPost() {
		return
	}
	config, err := configuredRegistrationSMTP()
	if err != nil {
		self.recoveryError(http.StatusServiceUnavailable, "smtp_incomplete", "邮件服务尚未配置完成，请联系管理员。", "Email service is not configured. Contact the administrator.")
		return
	}
	email := strings.ToLower(strings.TrimSpace(self.GetString("email")))
	user, _ := file.GetDb().GetUserByEmail(email)
	id, _, _, binding := passwordResetBinding(user)
	eligible := id > 0 && file.GetDb().IsUserActive(id)
	if !eligible {
		binding = "unbound"
	}
	// Issue a dummy ticket for unknown/disabled emails as well: responses and
	// cooldowns must not reveal whether an account exists.
	code, _, err := passwordResetCodes.issueBound(email, requestVisitorIP(self.Ctx), time.Now(), passwordResetTTL, passwordResetCooldown, binding)
	if err != nil {
		self.recoveryError(http.StatusTooManyRequests, "email_code_rate_limited", "验证码请求过于频繁，请稍后重试。", "Too many verification requests. Try again later.")
		return
	}
	// Queue dummy and real requests identically. SMTP latency must not let
	// callers distinguish existing accounts; workers and queue are bounded.
	failure := audit.Event{ActorType: "anonymous", ActorName: boundedAuditName(email), Action: "auth.password_reset.delivery_failed",
		ResourceType: "session", Result: audit.ResultFailure, ErrorCode: "email_send_failed", Error: audit.FailureName("email_send_failed", false), AuthMethod: "email_code", Stage: "email"}
	auditRequestMetadata(self.Ctx, &failure)
	audit.Configure(beego.AppConfig.String("audit_log_path"))
	if !queuePasswordResetDelivery(passwordResetDelivery{email: email, code: code, config: config, eligible: eligible, failure: failure}) {
		self.recoveryError(http.StatusServiceUnavailable, "email_code_unavailable", "邮件队列繁忙，请稍后重试。", "Email queue is busy. Try again later.")
		return
	}
	response := authJSONResponse(1, "password_reset_email_sent", "如果该邮箱已绑定有效账号，将收到找回验证码，请检查收件箱和垃圾邮件。", "If this email belongs to an active account, a recovery code will arrive. Check your inbox and spam folder.")
	response["retry_after"], response["expires_in"] = 60, 600
	self.Data["json"] = response
	self.ServeJSON()
}

func (self *LoginController) ResetPassword() {
	email, ownerID, ownerName := "", 0, ""
	defer func() {
		self.auditAuthResponse("auth.password_reset", email, "")
		if ownerID > 0 {
			audit.Configure(beego.AppConfig.String("audit_log_path"))
			event := audit.Event{ActorType: "user", ActorID: ownerID, OwnerUserID: ownerID, ActorName: ownerName,
				Action: "user.password_reset", ResourceType: "user", ResourceID: ownerID, Result: audit.ResultSuccess, AuthMethod: "email_code", Stage: "complete"}
			auditRequestMetadata(self.Ctx, &event)
			if err := audit.Append(event); err != nil {
				beego.Error("写入密码找回审计失败:", err)
			}
		}
	}()
	if !self.recoveryPost() {
		return
	}
	email = strings.ToLower(strings.TrimSpace(self.GetString("email")))
	challengeExpires, challengeSignature := loginChallengeFromForm(&self.Controller)
	payload, err := crypt.ParseLoginPayload(self.GetString("password"))
	if err != nil || !self.verifyLoginPayload(payload, challengeExpires, challengeSignature) {
		self.recoveryError(http.StatusBadRequest, "password_reset_expired", "安全请求无效或已过期，请刷新页面重试。", "Secure request is invalid or expired. Refresh the page.")
		return
	}
	if len(payload.Password) < 6 || len(payload.Password) > 128 {
		self.recoveryError(http.StatusBadRequest, "invalid_password", "新密码须为 6–128 个字节。", "The new password must contain 6–128 bytes.")
		return
	}
	// Do not let an old-password login race the device epoch bump and the
	// password update, then inherit the new epoch with an old credential.
	accountLoginMu.Lock()
	defer accountLoginMu.Unlock()
	user, _ := file.GetDb().GetUserByEmail(email)
	id, previous, name, binding := passwordResetBinding(user)
	if !passwordResetCodes.verifyBound(email, self.GetString("email_code"), time.Now(), binding) || id <= 0 || !file.GetDb().IsUserActive(id) {
		self.recoveryError(http.StatusBadRequest, "email_code_invalid", "邮箱验证码错误或已过期，请重新获取。", "The email code is incorrect or expired. Request a new one.")
		return
	}
	if _, _, err := rememberLoginDevices.RevokeAll(sessionPrincipalUser+":"+strconv.Itoa(id), time.Now()); err != nil {
		self.recoveryError(http.StatusServiceUnavailable, "password_reset_failed", "无法安全撤销旧登录设备，请联系管理员后重试。", "Existing sign-ins could not be revoked. Contact the administrator.")
		return
	}
	if err := file.GetDb().ResetUserPassword(id, email, previous, payload.Password); err != nil {
		beego.Error("保存找回密码失败:", err)
		self.recoveryError(http.StatusServiceUnavailable, "password_reset_failed", "密码重置未保存，请重新获取验证码；若仍失败请联系管理员。", "Password reset was not saved. Request a new code or contact the administrator.")
		return
	}
	ownerID, ownerName = id, name
	self.Data["json"] = authJSONResponse(1, "password_reset_success", "密码已重置，旧登录设备已退出，请重新登录。", "Password reset. Existing devices are signed out. Sign in again.")
	self.ServeJSON()
}
