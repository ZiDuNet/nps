package controllers

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/web/audit"
	"github.com/astaxie/beego"
)

func TestUserAndVKeyLoginGatesAndScopes(t *testing.T) {
	for _, tc := range []struct {
		name, username, password, users, vkeys, principal string
		want                                              bool
	}{
		{"username independent of VKey toggle", "alice", "user-pass", "true", "false", sessionPrincipalUser, true},
		{"email independent of VKey toggle", "ALICE@EXAMPLE.COM", "user-pass", "true", "false", sessionPrincipalUser, true},
		{"legacy VKey denied", "user", "client-key", "true", "false", "", false},
		{"owner VKey denied", "alice", "client-key", "true", "false", "", false},
		{"client credentials denied", "legacy", "legacy-pass", "true", "false", "", false},
		{"legacy user VKey despite client web credentials", "user", "client-key", "true", "true", sessionPrincipalClient, true},
		{"owner username and VKey", "alice", "client-key", "true", "true", sessionPrincipalClient, true},
		{"owner email and VKey", "alice@example.com", "client-key", "true", "true", sessionPrincipalClient, true},
		{"client name and VKey", "legacy", "client-key", "true", "true", sessionPrincipalClient, true},
		{"legacy client password", "legacy", "legacy-pass", "true", "true", sessionPrincipalClient, true},
		{"unrelated owner denied", "bob", "client-key", "true", "true", "", false},
		{"global user login disabled", "alice", "user-pass", "false", "true", "", false},
		{"global client login disabled", "user", "client-key", "false", "true", "", false},
		{"admin unaffected", "admin", "correct-password", "false", "false", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configureAuthAuditTest(t)
			_ = beego.AppConfig.Set("allow_user_login", tc.users)
			_ = beego.AppConfig.Set("allow_user_vkey_login", tc.vkeys)
			db := file.GetDb()
			db.JsonDb.Users.Store(7, &file.User{Id: 7, UserName: "alice", Email: "alice@example.com", Password: "user-pass", Status: true})
			db.JsonDb.Users.Store(8, &file.User{Id: 8, UserName: "bob", Password: "bob-pass", Status: true})
			db.JsonDb.Clients.Store(3, &file.Client{Id: 3, UserId: 7, Status: true, VerifyKey: "client-key", WebUserName: "legacy", WebPassword: "legacy-pass"})
			controller, _ := authAuditController(nil, nil)
			got, reason := controller.doLoginWithReason(tc.username, tc.password, "", false)
			if got != tc.want {
				t.Fatalf("authenticated=%v reason=%s want=%v", got, reason, tc.want)
			}
			if got && tc.principal != "" && controller.GetSession(sessionPrincipalKey) != tc.principal {
				t.Fatalf("wrong principal: %v", controller.GetSession(sessionPrincipalKey))
			}
			if got && tc.principal == sessionPrincipalClient {
				ids, ok := controller.GetSession("clientIds").(map[int]struct{})
				if !ok || len(ids) != 1 || controller.GetSession("clientId") != 3 || controller.GetSession("userId") != nil {
					t.Fatal("VKey login widened scope beyond one client")
				}
			}
		})
	}
}

func configureRecoveryTest(t *testing.T) {
	t.Helper()
	configureAuthAuditTest(t)
	for key, value := range map[string]string{
		"register_captcha": "false", "smtp_host": "smtp.example.test", "smtp_from": "nps@example.test",
		"smtp_security": "starttls", "smtp_username": "", "smtp_password": "", "smtp_port": "587",
	} {
		previous := beego.AppConfig.String(key)
		_ = beego.AppConfig.Set(key, value)
		t.Cleanup(func() { _ = beego.AppConfig.Set(key, previous) })
	}
	previousCodes, previousAttempts, previousDevices, previousQueue := passwordResetCodes, passwordResetAttempts, rememberLoginDevices, queuePasswordResetDelivery
	passwordResetCodes = newRegistrationEmailCodeStore(64)
	passwordResetAttempts = newRegistrationRateLimiter(64)
	rememberLoginDevices = newWebSessionRegistry(func() string { return filepath.Join(t.TempDir(), "devices.json") })
	t.Cleanup(func() {
		passwordResetCodes, passwordResetAttempts, rememberLoginDevices, queuePasswordResetDelivery = previousCodes, previousAttempts, previousDevices, previousQueue
	})
}

func encryptedRecoveryPassword(t *testing.T, nonce, password string) string {
	t.Helper()
	public, err := crypt.GetRSAPublicKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(public))
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(crypt.LoginPayload{Nonce: nonce, Timestamp: time.Now().UnixMilli(), Password: password})
	encrypted, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, parsed.(*rsa.PublicKey), payload, nil)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(encrypted)
}

func issueRecoveryChallenge(t *testing.T) *crypt.LoginChallenge {
	t.Helper()
	challenge, err := crypt.IssueLoginChallenge(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return challenge
}

func recoveryChallengeFields(challenge *crypt.LoginChallenge) map[string]string {
	return map[string]string{
		"nonce":               challenge.Nonce,
		"challenge_expires":   strconv.FormatInt(challenge.Expires, 10),
		"challenge_signature": challenge.Signature,
	}
}

func TestRecoveryCodeResponsesDoNotEnumerateAccounts(t *testing.T) {
	var responses []string
	for _, exists := range []bool{true, false} {
		t.Run(fmtBool(exists), func(t *testing.T) {
			configureRecoveryTest(t)
			if exists {
				file.GetDb().JsonDb.Users.Store(7, &file.User{Id: 7, UserName: "alice", Email: "alice@example.com", Password: "old-pass", Status: true})
			}
			queued := 0
			queuePasswordResetDelivery = func(job passwordResetDelivery) bool {
				queued++
				if job.eligible != exists || len(job.code) != 6 || job.email != "alice@example.com" {
					t.Fatalf("incorrect delivery job: eligible=%v", job.eligible)
				}
				return true
			}
			fields := recoveryChallengeFields(issueRecoveryChallenge(t))
			form := url.Values{"email": {"ALICE@EXAMPLE.COM"}, "nonce": {fields["nonce"]}, "challenge_expires": {fields["challenge_expires"]}, "challenge_signature": {fields["challenge_signature"]}}
			controller, recorder := authAuditController(form, nil)
			controller.ResetEmailCode()
			if recorder.Code != http.StatusOK || queued != 1 {
				t.Fatalf("code=%d queued=%d body=%s", recorder.Code, queued, recorder.Body)
			}
			responses = append(responses, recorder.Body.String())
			controller, recorder = authAuditController(form, nil)
			controller.ResetEmailCode()
			if recorder.Code != http.StatusTooManyRequests {
				t.Fatal("repeated code request bypassed cooldown")
			}
		})
	}
	if responses[0] != responses[1] {
		t.Fatal("public responses reveal account existence")
	}
}

func fmtBool(value bool) string {
	if value {
		return "existing email"
	}
	return "unknown email"
}

func TestRecoveryResetPreservesUserAndRevokesSessions(t *testing.T) {
	configureRecoveryTest(t)
	user := &file.User{Id: 7, UserName: "alice", Email: "alice@example.com", Password: "old-pass", Status: true,
		Remark: "keep-remark", APIKeyHash: "keep-api", MaxConn: 17, UploadLimit: 99, MaxClientNum: 8, MaxTunnelNum: 11, DashboardKeyHash: "keep-dashboard"}
	file.GetDb().JsonDb.Users.Store(7, user)
	id, _, _, binding := passwordResetBinding(user)
	code, _, err := passwordResetCodes.issueBound(user.Email, "192.0.2.1", time.Now(), passwordResetTTL, passwordResetCooldown, binding)
	if err != nil || id != 7 {
		t.Fatal(err)
	}
	fields := recoveryChallengeFields(issueRecoveryChallenge(t))
	form := url.Values{"email": {user.Email}, "nonce": {fields["nonce"]}, "email_code": {code}, "password": {encryptedRecoveryPassword(t, fields["nonce"], "new-password")}, "challenge_expires": {fields["challenge_expires"]}, "challenge_signature": {fields["challenge_signature"]}}
	controller, recorder := authAuditController(form, nil)
	controller.ResetPassword()
	if recorder.Code != http.StatusOK || controller.Data["json"].(map[string]interface{})["status"] != 1 {
		t.Fatalf("reset failed: %s", recorder.Body)
	}
	if user.Password != "new-password" || user.Email != "alice@example.com" || user.Remark != "keep-remark" || user.APIKeyHash != "keep-api" || user.MaxConn != 17 || user.UploadLimit != 99 || user.MaxClientNum != 8 || user.MaxTunnelNum != 11 || user.DashboardKeyHash != "keep-dashboard" {
		t.Fatal("password reset lost unrelated user fields")
	}
	if rememberLoginDevices.SessionEpochValid("user:7", 0) {
		t.Fatal("password reset kept pre-reset browser sessions")
	}
	if passwordResetCodes.verifyBound(user.Email, code, time.Now(), binding) {
		t.Fatal("recovery code can be replayed")
	}
	page, _ := audit.List(audit.Filter{Action: "user.password_reset"})
	if page.Total != 1 || page.Events[0].OwnerUserID != 7 {
		t.Fatal("verified recovery missing user-visible audit ownership")
	}
	journal, err := os.ReadFile(beego.AppConfig.String("audit_log_path"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{code, "new-password", "old-pass", form.Get("password"), fields["nonce"], fields["challenge_signature"]} {
		if bytes.Contains(journal, []byte(secret)) {
			t.Fatalf("audit leaked a recovery credential")
		}
	}
}

func TestRecoveryGuardsAndCodePurpose(t *testing.T) {
	for _, tc := range []struct{ name, config, value, nonce, password string }{
		{"registration disabled", "allow_user_register", "false", "browser-nonce", "encrypted"},
		{"missing browser nonce", "", "", "", "encrypted"},
		{"captcha required", "register_captcha", "true", "browser-nonce", "encrypted"},
		{"plaintext password forbidden", "", "", "browser-nonce", "plaintext-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configureRecoveryTest(t)
			if tc.config != "" {
				_ = beego.AppConfig.Set(tc.config, tc.value)
			}
			fields := recoveryChallengeFields(issueRecoveryChallenge(t))
			nonce := fields["nonce"]
			if tc.nonce == "" {
				nonce = ""
			}
			form := url.Values{"email": {"alice@example.com"}, "nonce": {nonce}, "password": {tc.password}, "challenge_expires": {fields["challenge_expires"]}, "challenge_signature": {fields["challenge_signature"]}}
			controller, recorder := authAuditController(form, nil)
			controller.ResetPassword()
			if recorder.Code < 400 {
				t.Fatalf("unguarded reset: %s", recorder.Body)
			}
		})
	}
	t.Run("codes bound to credential snapshot and bounded guesses", func(t *testing.T) {
		store := newRegistrationEmailCodeStore(1)
		code, _, _ := store.issueBound("a@example.com", "ip", time.Now(), time.Minute, 0, "first-password")
		if store.verify("a@example.com", code, time.Now()) || store.verifyBound("a@example.com", code, time.Now(), "changed-password") {
			t.Fatal("recovery ticket accepted outside its credential binding")
		}
		for i := 0; i < maxRegistrationEmailAttempts; i++ {
			store.verifyBound("a@example.com", "invalid", time.Now(), "first-password")
		}
		if store.verifyBound("a@example.com", code, time.Now(), "first-password") {
			t.Fatal("too many guesses did not invalidate code")
		}
	})
}

func TestRecoveryDisabledBlocksBothPostEndpointsAndPage(t *testing.T) {
	configureRecoveryTest(t)
	_ = beego.AppConfig.Set("allow_user_register", "false")
	for _, sendCode := range []bool{true, false} {
		controller, recorder := authAuditController(url.Values{"email": {"alice@example.com"}}, nil)
		if sendCode {
			controller.ResetEmailCode()
		} else {
			controller.ResetPassword()
		}
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("disabled recovery endpoint returned %d", recorder.Code)
		}
	}
	controller, recorder := authAuditController(nil, nil)
	controller.Ctx.Request.Method = http.MethodGet
	controller.Recover()
	if recorder.Code != http.StatusFound || !strings.HasSuffix(recorder.Header().Get("Location"), "/login/index") {
		t.Fatal("disabled recovery page did not return to sign-in")
	}
}

func TestVKeyToggleAlsoBlocksExistingClientRememberCredentials(t *testing.T) {
	configureRecoveryTest(t)
	file.GetDb().JsonDb.Clients.Store(3, &file.Client{Id: 3, Status: true, VerifyKey: "client-key"})
	controller, _ := authAuditController(nil, map[interface{}]interface{}{"auth": true, "isAdmin": false, sessionPrincipalKey: sessionPrincipalClient, "clientId": 3})
	claims := &rememberLoginClaims{Principal: sessionPrincipalClient, ClientID: 3, Username: "user"}
	if _, err := rememberedLoginCredential(claims); err != nil {
		t.Fatalf("baseline client remember credential invalid: %v", err)
	}
	_ = beego.AppConfig.Set("allow_user_vkey_login", "false")
	if _, err := rememberedLoginCredential(claims); err == nil {
		t.Fatal("disabled VKey login still restores client remember credential")
	}
	if browserSessionSecurityValid(&controller.Controller, nil, time.Now()) {
		t.Fatal("disabled VKey login kept existing client browser session")
	}
}

func TestRevokedSessionCannotIssueNewRememberCredential(t *testing.T) {
	configureRecoveryTest(t)
	file.GetDb().JsonDb.Users.Store(7, &file.User{Id: 7, UserName: "alice", Email: "alice@example.com", Password: "new-pass", Status: true})
	controller, recorder := authAuditController(nil, map[interface{}]interface{}{
		"auth": true, "isAdmin": false, sessionPrincipalKey: sessionPrincipalUser,
		"userId": 7, "username": "alice", sessionSecurityEpochKey: 0,
	})
	_, epoch, err := rememberLoginDevices.RevokeAll("user:7", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := issueRememberLoginCookie(&controller.Controller, controller.Ctx, time.Now()); err == nil {
		t.Fatal("revoked session adopted the new security epoch and issued a remembered sign-in")
	}
	if len(recorder.Result().Cookies()) != 0 {
		t.Fatal("revoked session set a remember cookie")
	}
	controller.SetSession(sessionSecurityEpochKey, int(epoch))
	if err := issueRememberLoginCookie(&controller.Controller, controller.Ctx, time.Now()); err != nil {
		t.Fatalf("a newly authenticated session could not issue a remember cookie: %v", err)
	}
}

func TestRegularPasswordChangePreservesEmailAndUserLimits(t *testing.T) {
	configureRecoveryTest(t)
	file.GetDb().JsonDb.Users.Store(7, &file.User{Id: 7, UserName: "alice", Password: "old-pass", Email: "alice@example.com", Status: true, MaxConn: 9, UploadLimit: 10, DownloadLimit: 11})
	login, _ := authAuditController(url.Values{"new_password": {"new-pass"}, "confirm_password": {"new-pass"}, "current_password": {"old-pass"}}, map[interface{}]interface{}{"auth": true, "isAdmin": false, sessionPrincipalKey: sessionPrincipalUser, "userId": 7})
	controller := &UserController{BaseController: BaseController{Controller: login.Controller}}
	runClientPermissionAction(t, controller.ChangePassword)
	user, _ := file.GetDb().GetUser(7)
	if user.Password != "new-pass" || user.Email != "alice@example.com" || user.MaxConn != 9 || user.UploadLimit != 10 || user.DownloadLimit != 11 {
		t.Fatal("regular password change discarded recovery email or limits")
	}
}

func TestRecoveryPostRejectsOversizedRequests(t *testing.T) {
	configureRecoveryTest(t)
	controller, recorder := authAuditController(nil, nil)
	controller.Ctx.Request = httptest.NewRequest(http.MethodPost, "http://console.test/login/recover", strings.NewReader(strings.Repeat("x", 5000)))
	controller.ResetPassword()
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized reset request returned %d", recorder.Code)
	}
}

func TestRecoveryPageAndLoginEntryFollowRegistrationSwitch(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		tpl := template.Must(template.New("index.html").Funcs(template.FuncMap{"create_captcha": func() template.HTML { return "" }}).ParseFiles("../views/login/index.html"))
		var output bytes.Buffer
		if err := tpl.Execute(&output, map[string]interface{}{"register_allow": enabled, "web_base_url": "/panel"}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), `href="/panel/login/recover"`) != enabled {
			t.Fatal("recovery entry ignores registration switch")
		}
	}
	tpl := template.Must(template.New("recover.html").Funcs(template.FuncMap{"create_captcha": func() template.HTML { return "" }}).ParseFiles("../views/login/recover.html"))
	var output bytes.Buffer
	if err := tpl.Execute(&output, map[string]interface{}{"web_base_url": "/panel", "email_ready": true, "captcha_open": true, "public_key": "key\nline", "login_nonce": "nonce"}); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{`id="recover-form"`, `id="auth-email"`, `id="auth-confirm"`, `aria-live="polite"`, `nps-password-reset.js`, `auth-network-background`, `publicKey: "key\nline"`} {
		if !strings.Contains(output.String(), marker) {
			t.Fatalf("missing recovery UI feature: %s", marker)
		}
	}
}
