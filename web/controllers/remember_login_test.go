package controllers

import (
	"ehang.io/nps/web/audit"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/astaxie/beego"
	beecontext "github.com/astaxie/beego/context"
)

func TestRememberLoginTokenRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_800_000_000, 0)
	claims := rememberLoginClaims{
		Principal: sessionPrincipalUser,
		UserID:    7,
		Username:  "alice",
	}
	claims.Fingerprint = rememberLoginFingerprint(key, claims, "secret")

	token, err := encodeRememberLoginToken(key, claims, now)
	if err != nil {
		t.Fatalf("encode token: %v", err)
	}
	decoded, err := decodeRememberLoginToken(key, token, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if decoded.UserID != claims.UserID || decoded.Username != claims.Username ||
		decoded.ExpiresAt != now.Add(rememberLoginDuration).Unix() {
		t.Fatalf("decoded token = %#v", decoded)
	}
	if _, err := decodeRememberLoginToken(key, token, now.Add(rememberLoginDuration)); err == nil {
		t.Fatal("expired token was accepted")
	}
	if _, err := decodeRememberLoginToken([]byte("another-key-that-is-not-the-same"), token, now); err == nil {
		t.Fatal("token signed with another key was accepted")
	}
	tampered := token[:len(token)-1] + "A"
	if tampered == token {
		tampered = token[:len(token)-1] + "B"
	}
	if _, err := decodeRememberLoginToken(key, tampered, now); err == nil {
		t.Fatal("tampered token was accepted")
	}
}

func TestRememberLoginFingerprintBindsCredential(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	claims := rememberLoginClaims{Principal: "admin", Username: "root"}
	first := rememberLoginFingerprint(key, claims, "password-a")
	second := rememberLoginFingerprint(key, claims, "password-b")
	if first == second {
		t.Fatal("credential fingerprint did not change with the password")
	}
	claims.Fingerprint = first
	if rememberLoginFingerprint(key, claims, "password-a") != first {
		t.Fatal("stable credential fingerprint changed unexpectedly")
	}
}

func TestRememberLoginCookieAttributesAndPath(t *testing.T) {
	previousBase := beego.AppConfig.String("web_base_url")
	previousSSL := beego.AppConfig.String("web_open_ssl")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("web_base_url", previousBase)
		_ = beego.AppConfig.Set("web_open_ssl", previousSSL)
	})
	_ = beego.AppConfig.Set("web_base_url", "/nps/")
	_ = beego.AppConfig.Set("web_open_ssl", "false")

	request := httptest.NewRequest(http.MethodGet, "http://example.test/nps/login/index", nil)
	request.Header.Set("X-Forwarded-Proto", "https")
	cookie := rememberLoginCookie(request, "token", time.Unix(1_800_000_000, 0))
	if cookie.Path != "/nps" || !cookie.HttpOnly || !cookie.Secure ||
		cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != int(rememberLoginDuration.Seconds()) {
		t.Fatalf("remember cookie attributes = %#v", cookie)
	}
}

func TestRestoreRememberLoginCreatesAuthenticatedSession(t *testing.T) {
	previousAudit := beego.AppConfig.String("audit_log_path")
	_ = beego.AppConfig.Set("audit_log_path", filepath.Join(t.TempDir(), "audit.log"))
	t.Cleanup(func() { _ = beego.AppConfig.Set("audit_log_path", previousAudit); audit.Configure("") })
	previousKeyPath := beego.AppConfig.String("instance_key_file")
	previousUsername := beego.AppConfig.String("web_username")
	previousPassword := beego.AppConfig.String("web_password")
	previousTOTP := beego.AppConfig.String("totp_secret")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("instance_key_file", previousKeyPath)
		_ = beego.AppConfig.Set("web_username", previousUsername)
		_ = beego.AppConfig.Set("web_password", previousPassword)
		_ = beego.AppConfig.Set("totp_secret", previousTOTP)
	})
	_ = beego.AppConfig.Set("instance_key_file", filepath.Join(t.TempDir(), "instance.key"))
	_ = beego.AppConfig.Set("web_username", "root")
	_ = beego.AppConfig.Set("web_password", "password")
	_ = beego.AppConfig.Set("totp_secret", "")

	key, err := rememberLoginKey()
	if err != nil {
		t.Fatalf("load remember login key: %v", err)
	}
	now := time.Unix(1_800_000_000, 0)
	claims := rememberLoginClaims{Principal: sessionPrincipalUser, Username: "root"}
	claims.Fingerprint = rememberLoginFingerprint(key, claims, "password\x00")
	token, err := encodeRememberLoginToken(key, claims, now)
	if err != nil {
		t.Fatalf("encode token: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://example.test/index/index", nil)
	request.AddCookie(&http.Cookie{Name: rememberLoginCookieName, Value: token})
	recorder := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(recorder, request)
	store := &clientPermissionSession{values: map[interface{}]interface{}{"stale": true}}
	controller := &beego.Controller{CruSession: store}

	restored, err := restoreRememberLogin(controller, ctx, now.Add(time.Minute))
	if err != nil || !restored {
		t.Fatalf("restore remember login = %v, %v", restored, err)
	}
	if !sessionBool(store.Get("auth")) || !sessionBool(store.Get("isAdmin")) || store.Get("stale") != nil {
		t.Fatalf("restored session values = %#v", store.values)
	}
	page, err := audit.List(audit.Filter{})
	if err != nil || page.Total != 1 || page.Events[0].Action != "auth.remember.restore" || page.Events[0].ActorName != "root" || page.Events[0].ActorType != "admin" {
		t.Fatalf("remember restore audit: %+v %v", page, err)
	}
	if _, err := restoreRememberLogin(controller, ctx, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	page, _ = audit.List(audit.Filter{})
	if page.Total != 1 {
		t.Fatal("an already authenticated session generated another restore event")
	}
}
