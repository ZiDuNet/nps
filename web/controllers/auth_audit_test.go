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
	"strings"
	"testing"
	"time"

	"ehang.io/nps/bridge"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/server"
	"ehang.io/nps/web/audit"
	"github.com/astaxie/beego"
	beecontext "github.com/astaxie/beego/context"
)

func configureAuthAuditTest(t *testing.T) string {
	t.Helper()
	keys := map[string]string{
		"audit_log_path": filepath.Join(t.TempDir(), "logs", "audit.log"),
		"web_username":   "admin", "web_password": "correct-password", "totp_secret": "",
		"force_pow": "false", "open_captcha": "false", "allow_user_login": "true",
		"allow_user_vkey_login": "true", "allow_x_real_ip": "true", "http_forwarded_trust_mode": "strict",
		"trusted_proxy_ips": "192.0.2.10", "login_ban_time": "0", "login_max_body": "1024",
		"login_max_fail_times": "10", "allow_user_register": "true", "register_captcha": "true",
		"instance_key_file": filepath.Join(t.TempDir(), "instance.key"),
	}
	for key, value := range keys {
		previous := beego.AppConfig.String(key)
		_ = beego.AppConfig.Set(key, value)
		t.Cleanup(func() { _ = beego.AppConfig.Set(key, previous) })
	}
	previousBridge, previousSessions := server.Bridge, beego.GlobalSessions
	server.Bridge, beego.GlobalSessions = &bridge.Bridge{}, nil
	t.Cleanup(func() {
		server.Bridge, beego.GlobalSessions = previousBridge, previousSessions
		loginAttempts.removeAllBans()
		audit.Configure("")
	})
	loginAttempts.removeAllBans()
	useClientPermissionTestDb(t)
	return keys["audit_log_path"]
}

func authAuditController(form url.Values, values map[interface{}]interface{}) (*LoginController, *httptest.ResponseRecorder) {
	request := httptest.NewRequest(http.MethodPost, "http://console.test/login/verify?api_token=DO-NOT-LOG-QUERY", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", "AuditBrowser/1.0")
	request.Header.Set("X-Real-IP", "2001:db8::123")
	request.Header.Set("Authorization", "Bearer DO-NOT-LOG-HEADER")
	request.RemoteAddr = "192.0.2.10:50123"
	recorder := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(recorder, request)
	if values == nil {
		values = map[interface{}]interface{}{}
	}
	store := &clientPermissionSession{values: values}
	return &LoginController{Controller: beego.Controller{Ctx: ctx, Data: map[interface{}]interface{}{}, CruSession: store}}, recorder
}

func onlyAuthAuditEvent(t *testing.T) *audit.Event {
	t.Helper()
	page, err := audit.List(audit.Filter{})
	if err != nil || page.Total != 1 {
		t.Fatalf("want exactly one event: %+v %v", page, err)
	}
	return page.Events[0]
}

func TestLoginAuditCoversEarlyFailuresWithoutCredentials(t *testing.T) {
	for _, tc := range []struct{ name, config, value, code, stage string }{
		{"wrong-password", "", "", "invalid_credentials", "password"},
		{"pow", "force_pow", "true", "pow_invalid", "pow"},
		{"captcha", "open_captcha", "true", "captcha_invalid", "captcha"},
		{"size", "login_max_body", "10", "request_too_large", "request"},
		{"totp", "totp_secret", "JBSWY3DPEHPK3PXP", "totp_invalid", "totp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := configureAuthAuditTest(t)
			if tc.config != "" {
				_ = beego.AppConfig.Set(tc.config, tc.value)
			}
			password := "DO-NOT-LOG-PASSWORD"
			if tc.name == "totp" {
				password = "correct-password"
			}
			controller, recorder := authAuditController(url.Values{"username": {"admin"}, "password": {password}, "totp": {"DO-NOT-LOG-TOTP"}, "pow_solution": {"DO-NOT-LOG-POW"}}, nil)
			controller.Verify()
			event := onlyAuthAuditEvent(t)
			if event.Action != "auth.login" || event.Result != audit.ResultFailure || event.ErrorCode != tc.code || event.Stage != tc.stage || event.ActorType != "anonymous" || event.OwnerUserID != 0 {
				t.Fatalf("failure event = %+v", event)
			}
			if event.Method != "POST" || event.Path != "/login/verify" || event.UserAgent != "AuditBrowser/1.0" || event.SourceIP != "2001:db8::123" || event.RemoteAddr != "192.0.2.10:50123" || event.RequestID == "" || event.RequestID != recorder.Header().Get("X-Request-ID") {
				t.Fatalf("request metadata = %+v", event)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{password, "DO-NOT-LOG-TOTP", "DO-NOT-LOG-POW", "DO-NOT-LOG-QUERY", "DO-NOT-LOG-HEADER"} {
				if bytes.Contains(data, []byte(secret)) {
					t.Errorf("journal contains secret %q", secret)
				}
			}
		})
	}
}

func TestLoginAuditExpiredPayloadAndRateLimit(t *testing.T) {
	t.Run("expired payload", func(t *testing.T) {
		configureAuthAuditTest(t)
		payload, _ := json.Marshal(crypt.LoginPayload{Password: "private-password", Nonce: "non-matching-nonce", Timestamp: time.Now().UnixMilli()})
		publicPEM, err := crypt.GetRSAPublicKeyPEM()
		if err != nil {
			t.Fatal(err)
		}
		block, _ := pem.Decode([]byte(publicPEM))
		if block == nil {
			t.Fatal("invalid public key")
		}
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		cipher, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, parsed.(*rsa.PublicKey), payload, nil)
		if err != nil {
			t.Fatal(err)
		}
		controller, _ := authAuditController(url.Values{"username": {"admin"}, "password": {base64.StdEncoding.EncodeToString(cipher)}}, nil)
		controller.Verify()
		if event := onlyAuthAuditEvent(t); event.ErrorCode != "login_expired" {
			t.Fatalf("expired event = %+v", event)
		}
	})
	t.Run("rate limit", func(t *testing.T) {
		configureAuthAuditTest(t)
		_ = beego.AppConfig.Set("login_max_fail_times", "1")
		controller, _ := authAuditController(url.Values{"username": {"admin"}, "password": {"bad"}}, nil)
		controller.Verify()
		controller, _ = authAuditController(url.Values{"username": {"admin"}, "password": {"correct-password"}}, nil)
		controller.Verify()
		page, _ := audit.List(audit.Filter{})
		if page.Total != 2 || page.Events[0].ErrorCode != "login_rate_limited" {
			t.Fatalf("rate limit events: %+v", page.Events)
		}
		if response := controller.Data["json"].(map[string]interface{}); response["code"] != "invalid_credentials" {
			t.Fatal("internal reason leaked into public response")
		}
	})
}

func TestSuccessfulUserLoginAndLogoutAuditHaveTrustedOwnership(t *testing.T) {
	configureAuthAuditTest(t)
	file.GetDb().JsonDb.Users.Store(7, &file.User{Id: 7, UserName: "alice", Password: "private-password", Status: true})
	controller, _ := authAuditController(url.Values{"username": {"alice"}, "password": {"private-password"}}, nil)
	controller.Verify()
	event := onlyAuthAuditEvent(t)
	if event.ActorName != "alice" || event.ActorType != "user" || event.ActorID != 7 || event.OwnerUserID != 7 || event.Result != audit.ResultSuccess {
		t.Fatalf("login identity: %+v", event)
	}
	controller.Out()
	page, _ := audit.List(audit.Filter{Action: "auth.logout"})
	if page.Total != 1 || page.Events[0].OwnerUserID != 7 || page.Events[0].ActorName != "alice" {
		t.Fatalf("logout identity: %+v", page)
	}
	if sessionBool(controller.GetSession("auth")) {
		t.Fatal("logout kept authenticated session")
	}
}

func TestRememberAuditInvalidCookieIsNotAttributed(t *testing.T) {
	path := configureAuthAuditTest(t)
	controller, _ := authAuditController(nil, nil)
	controller.Ctx.Request.AddCookie(&http.Cookie{Name: rememberLoginCookieName, Value: "DO-NOT-LOG-COOKIE"})
	if restored, err := restoreRememberLogin(&controller.Controller, controller.Ctx, time.Now()); restored || err == nil {
		t.Fatal("invalid remember cookie was accepted")
	}
	event := onlyAuthAuditEvent(t)
	if event.Action != "auth.remember.restore" || event.ErrorCode != "remember_invalid" || event.ActorType != "anonymous" || event.OwnerUserID != 0 {
		t.Fatalf("invalid remember event: %+v", event)
	}
	data, _ := os.ReadFile(path)
	if bytes.Contains(data, []byte("DO-NOT-LOG-COOKIE")) {
		t.Fatal("journal contains remember cookie")
	}
}

func TestRegisterAuditIncludesValidationFailures(t *testing.T) {
	configureAuthAuditTest(t)
	controller, _ := authAuditController(url.Values{"username": {"new-user"}, "password": {"private-password"}}, nil)
	controller.Register()
	event := onlyAuthAuditEvent(t)
	if event.Action != "auth.register" || event.ErrorCode != "registration_captcha_invalid" || event.Stage != "captcha" {
		t.Fatalf("register validation: %+v", event)
	}
}

func TestAuditTemplateContainsSharedActionCatalogAndResponsiveView(t *testing.T) {
	tpl, err := template.ParseFiles("../views/audit/list.html")
	if err != nil {
		t.Fatal(err)
	}
	var page bytes.Buffer
	err = tpl.Execute(&page, map[string]interface{}{"audit_action_groups": audit.ActionGroups(false), "connection_action_groups": audit.ActionGroups(true), "version": "test", "web_base_url": "/panel"})
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{`value="auth.login"`, `value="global.login_ban.remove"`, `value="session"`, `nps-audit.css?v=test`, `data-audit-copy`, `aria-live="polite"`} {
		if !strings.Contains(page.String(), marker) {
			t.Errorf("audit page missing %s", marker)
		}
	}
}
