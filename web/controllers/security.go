package controllers

import (
	"crypto/subtle"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"ehang.io/nps/lib/crypt"
	"github.com/astaxie/beego"
	beecontext "github.com/astaxie/beego/context"
)

type SecurityController struct {
	BaseController
}

type webSessionDeviceView struct {
	ID         string `json:"id"`
	Device     string `json:"device"`
	Browser    string `json:"browser"`
	OS         string `json:"os"`
	IP         string `json:"ip"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
	ExpiresAt  string `json:"expires_at"`
	IsCurrent  bool   `json:"is_current"`
}

func (s *SecurityController) Settings() {
	if !s.RequireAdmin() {
		return
	}
	if s.Ctx.Request.Method != http.MethodGet {
		s.rejectRequest(http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.Data["menu"] = "global"
	s.Data["security_global_settings"] = true
	s.Data["totp_bound"] = strings.TrimSpace(beego.AppConfig.String("totp_secret")) != ""
	s.Data["totp_account"] = strings.TrimSpace(beego.AppConfig.String("web_username"))
	s.Data["pow_enabled"] = beego.AppConfig.DefaultBool("force_pow", false)
	s.Data["pow_bits"] = powDifficulty()
	s.SetInfo("security settings")
	s.display("global/security")
}

func (s *SecurityController) DeviceList() {
	if s.Ctx.Request.Method != http.MethodGet {
		s.rejectRequest(http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	principalKey := currentSessionPrincipalKey(&s.Controller)
	if principalKey == "" {
		s.rejectRequest(http.StatusForbidden, "当前会话不支持设备管理")
		return
	}
	records, err := rememberLoginDevices.List(principalKey, time.Now())
	if err != nil {
		s.AjaxErr("读取登录设备失败: " + err.Error())
		return
	}
	currentID := ""
	if claims, err := rememberLoginCookieClaims(s.Ctx, time.Now()); err == nil {
		if rememberLoginPrincipalKey(claims) == principalKey {
			currentID = claims.DeviceID
		}
	}
	rows := make([]webSessionDeviceView, 0, len(records))
	for _, record := range records {
		browser, osName := describeSessionUserAgent(record.UserAgent)
		rows = append(rows, webSessionDeviceView{
			ID:         record.ID,
			Device:     sessionDeviceLabel(record.UserAgent),
			Browser:    browser,
			OS:         osName,
			IP:         record.IP,
			CreatedAt:  formatSessionTime(record.CreatedAt),
			LastSeenAt: formatSessionTime(record.LastSeenAt),
			ExpiresAt:  formatSessionTime(record.ExpiresAt),
			IsCurrent:  record.ID == currentID,
		})
	}
	s.Data["json"] = s.withRequestMeta(map[string]interface{}{
		"status":  1,
		"devices": rows,
		"total":   len(rows),
	})
	s.ServeJSON()
}

func (s *SecurityController) RevokeDevice() {
	if !s.RequirePost() {
		return
	}
	principalKey := currentSessionPrincipalKey(&s.Controller)
	if principalKey == "" {
		s.rejectRequest(http.StatusForbidden, "当前会话不支持设备管理")
		return
	}
	id := strings.TrimSpace(s.GetString("id"))
	currentID := ""
	if claims, claimsErr := rememberLoginCookieClaims(s.Ctx, time.Now()); claimsErr == nil {
		currentID = claims.DeviceID
	}
	revoked, err := rememberLoginDevices.Revoke(principalKey, id)
	if err != nil {
		s.AjaxErr("撤销登录设备失败: " + err.Error())
		return
	}
	if !revoked {
		s.AjaxErr("登录设备不存在或已被撤销")
		return
	}
	isCurrent := currentID != "" && currentID == id
	if isCurrent {
		clearAuthenticationSession(s.DelSession)
		clearRememberLoginCookie(s.Ctx.Request, s.Ctx.ResponseWriter)
		releaseControllerCookieSession(&s.Controller, s.Ctx)
	}
	s.auditMutation("security.remember_device.revoke", "session", 0, 0, "", nil, map[string]string{"device_id": id})
	s.Data["json"] = s.withRequestMeta(map[string]interface{}{
		"status": 1, "msg": "登录设备已撤销", "logout": isCurrent,
	})
	s.ServeJSON()
}

func (s *SecurityController) RevokeAllDevices() {
	if !s.RequirePost() {
		return
	}
	principalKey := currentSessionPrincipalKey(&s.Controller)
	if principalKey == "" {
		s.rejectRequest(http.StatusForbidden, "当前会话不支持设备管理")
		return
	}
	count, epoch, err := rememberLoginDevices.RevokeAll(principalKey, time.Now())
	if err != nil {
		s.AjaxErr("退出所有设备失败: " + err.Error())
		return
	}
	clearAuthenticationSession(s.DelSession)
	clearRememberLoginCookie(s.Ctx.Request, s.Ctx.ResponseWriter)
	releaseControllerCookieSession(&s.Controller, s.Ctx)
	s.auditMutation("security.remember_device.revoke_all", "session", 0, 0, "", nil, map[string]string{
		"devices": strconv.Itoa(count),
		"epoch":   strconv.FormatUint(epoch, 10),
	})
	s.Data["json"] = s.withRequestMeta(map[string]interface{}{
		"status": 1, "msg": "已退出所有设备", "revoked": count, "logout": true,
	})
	s.ServeJSON()
}

func (s *SecurityController) TOTPStart() {
	if !s.RequirePost() || !s.RequireAdmin() {
		return
	}
	if err := s.verifyAdminPassword(s.GetString("password")); err != nil {
		s.AjaxErr(err.Error())
		return
	}
	currentSecret := strings.TrimSpace(beego.AppConfig.String("totp_secret"))
	if currentSecret != "" {
		if ok, _ := crypt.ValidateTOTPCode(currentSecret, strings.TrimSpace(s.GetString("current_code"))); !ok {
			s.AjaxErr("请输入当前认证器中的 6 位动态验证码")
			return
		}
	}
	secret, err := crypt.GenerateTOTPSecret()
	if err != nil {
		s.AjaxErr("生成 TOTP 密钥失败")
		return
	}
	account := strings.TrimSpace(beego.AppConfig.String("web_username"))
	if account == "" {
		account = "admin"
	}
	uri := crypt.BuildTOTPURI("NPS", account, secret)
	s.Data["json"] = s.withRequestMeta(map[string]interface{}{
		"status":  1,
		"msg":     "请在认证器中添加以下密钥，再输入动态验证码完成绑定",
		"secret":  secret,
		"uri":     uri,
		"account": account,
	})
	s.ServeJSON()
}

func (s *SecurityController) TOTPConfirm() {
	if !s.RequirePost() || !s.RequireAdmin() {
		return
	}
	if err := s.verifyAdminPassword(s.GetString("password")); err != nil {
		s.AjaxErr(err.Error())
		return
	}
	currentSecret := strings.TrimSpace(beego.AppConfig.String("totp_secret"))
	if currentSecret != "" {
		if ok, _ := crypt.ValidateTOTPCode(currentSecret, strings.TrimSpace(s.GetString("current_code"))); !ok {
			s.AjaxErr("当前 TOTP 验证码不正确")
			return
		}
	}
	secret := strings.ToUpper(strings.TrimSpace(s.GetString("secret")))
	if !crypt.IsValidTOTPSecret(secret) {
		s.AjaxErr("TOTP 密钥格式无效，请重新开始绑定")
		return
	}
	if ok, _ := crypt.ValidateTOTPCode(secret, strings.TrimSpace(s.GetString("code"))); !ok {
		s.AjaxErr("认证器动态验证码不正确，请确认设备时间后重试")
		return
	}
	if _, err := writeServerConfig(serverConfigPath(), []serverConfigUpdate{{Key: "totp_secret", Value: secret}}); err != nil {
		s.AjaxErr("保存 TOTP 配置失败: " + err.Error())
		return
	}
	_ = beego.AppConfig.Set("totp_secret", secret)
	if err := refreshAdminSessionsAfterCredentialChange(&s.Controller, s.Ctx, time.Now()); err != nil {
		s.AjaxErr("TOTP 已保存，但旧登录会话清理失败，请立即在“登录设备与免登凭证”中退出所有设备")
		return
	}
	s.auditMutation("security.totp.bind", "nps.conf", 0, 0, "", nil, nil)
	s.Data["json"] = s.withRequestMeta(map[string]interface{}{
		"status": 1, "msg": "TOTP 已绑定并立即生效", "bound": true,
	})
	s.ServeJSON()
}

func (s *SecurityController) TOTPDisable() {
	if !s.RequirePost() || !s.RequireAdmin() {
		return
	}
	if err := s.verifyAdminPassword(s.GetString("password")); err != nil {
		s.AjaxErr(err.Error())
		return
	}
	currentSecret := strings.TrimSpace(beego.AppConfig.String("totp_secret"))
	if currentSecret == "" {
		s.AjaxErr("当前未绑定 TOTP")
		return
	}
	if ok, _ := crypt.ValidateTOTPCode(currentSecret, strings.TrimSpace(s.GetString("code"))); !ok {
		s.AjaxErr("认证器动态验证码不正确")
		return
	}
	if _, err := writeServerConfig(serverConfigPath(), []serverConfigUpdate{{Key: "totp_secret", Value: "", Omit: true}}); err != nil {
		s.AjaxErr("关闭 TOTP 失败: " + err.Error())
		return
	}
	_ = beego.AppConfig.Set("totp_secret", "")
	if err := refreshAdminSessionsAfterCredentialChange(&s.Controller, s.Ctx, time.Now()); err != nil {
		s.AjaxErr("TOTP 已关闭，但旧登录会话清理失败，请立即在“登录设备与免登凭证”中退出所有设备")
		return
	}
	s.auditMutation("security.totp.disable", "nps.conf", 0, 0, "", nil, nil)
	s.Data["json"] = s.withRequestMeta(map[string]interface{}{
		"status": 1, "msg": "TOTP 已关闭并立即生效", "bound": false,
	})
	s.ServeJSON()
}

func (s *SecurityController) SavePoW() {
	if !s.RequirePost() || !s.RequireAdmin() {
		return
	}
	enabled := strings.EqualFold(strings.TrimSpace(s.GetString("force_pow")), "true") ||
		strings.TrimSpace(s.GetString("force_pow")) == "1"
	bits, err := strconv.Atoi(strings.TrimSpace(s.GetString("pow_bits")))
	if err != nil || bits < 8 || bits > 28 {
		s.AjaxErr("PoW 难度必须是 8-28 之间的整数")
		return
	}
	updates := []serverConfigUpdate{
		{Key: "force_pow", Value: strconv.FormatBool(enabled)},
		{Key: "pow_bits", Value: strconv.Itoa(bits)},
	}
	changed, err := writeServerConfig(serverConfigPath(), updates)
	if err != nil {
		s.AjaxErr("保存 PoW 配置失败: " + err.Error())
		return
	}
	_ = beego.AppConfig.Set("force_pow", strconv.FormatBool(enabled))
	_ = beego.AppConfig.Set("pow_bits", strconv.Itoa(bits))
	s.auditMutation("security.pow.update", "nps.conf", 0, 0, "", nil, map[string]string{"changed": strconv.Itoa(len(changed))})
	s.Data["json"] = s.withRequestMeta(map[string]interface{}{
		"status": 1, "msg": "登录 PoW 配置已保存并立即生效", "changed": changed,
	})
	s.ServeJSON()
}

func (s *SecurityController) verifyAdminPassword(password string) error {
	expected := beego.AppConfig.String("web_password")
	if password == "" || expected == "" ||
		subtle.ConstantTimeCompare([]byte(password), []byte(expected)) != 1 {
		return &securityValidationError{message: "管理员登录密码验证失败"}
	}
	return nil
}

func refreshAdminSessionsAfterCredentialChange(controller *beego.Controller, ctx *beecontext.Context, now time.Time) error {
	if controller == nil {
		return nil
	}
	_, epoch, err := rememberLoginDevices.RevokeAll("admin", now)
	if err != nil {
		return err
	}
	controller.SetSession(sessionSecurityEpochKey, int(epoch))
	if ctx == nil || ctx.Request == nil {
		return nil
	}
	if _, err := ctx.Request.Cookie(rememberLoginCookieName); err != nil {
		return nil
	}
	return issueRememberLoginCookie(controller, ctx, now)
}

type securityValidationError struct {
	message string
}

func (e *securityValidationError) Error() string {
	return e.message
}

func formatSessionTime(unix int64) string {
	if unix <= 0 {
		return "-"
	}
	return time.Unix(unix, 0).Local().Format("2006-01-02 15:04:05")
}

func sessionDeviceLabel(userAgent string) string {
	browser, osName := describeSessionUserAgent(userAgent)
	if browser == "未知浏览器" && osName == "未知系统" {
		return "未知设备"
	}
	return browser + " · " + osName
}

func describeSessionUserAgent(userAgent string) (string, string) {
	ua := strings.ToLower(userAgent)
	browser := "未知浏览器"
	switch {
	case strings.Contains(ua, "edg/"):
		browser = "Microsoft Edge"
	case strings.Contains(ua, "opr/") || strings.Contains(ua, "opera"):
		browser = "Opera"
	case strings.Contains(ua, "firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "chrome/") || strings.Contains(ua, "crios/"):
		browser = "Chrome"
	case strings.Contains(ua, "safari/") && !strings.Contains(ua, "chrome/"):
		browser = "Safari"
	case strings.Contains(ua, "micromessenger"):
		browser = "微信"
	case strings.Contains(ua, "curl/"):
		browser = "curl"
	case strings.Contains(ua, "wget/"):
		browser = "wget"
	}
	osName := "未知系统"
	switch {
	case strings.Contains(ua, "windows"):
		osName = "Windows"
	case strings.Contains(ua, "android"):
		osName = "Android"
	case strings.Contains(ua, "iphone") || strings.Contains(ua, "ipad") || strings.Contains(ua, "ios"):
		osName = "iOS / iPadOS"
	case strings.Contains(ua, "mac os") || strings.Contains(ua, "macintosh"):
		osName = "macOS"
	case strings.Contains(ua, "linux"):
		osName = "Linux"
	default:
		osName = runtime.GOOS
	}
	return browser, osName
}
