package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego"
	beecontext "github.com/astaxie/beego/context"
)

func TestDashboardAccessKeyGenerationAndValidation(t *testing.T) {
	key, hash, err := generateDashboardAccessKey("custom_key-1234567890")
	if err != nil {
		t.Fatalf("generateDashboardAccessKey: %v", err)
	}
	if key != dashboardAccessKeyPrefix+"custom_key-1234567890" || !validDashboardAccessKey(hash, key) {
		t.Fatalf("custom dashboard key did not validate: key=%q hash=%q", key, hash)
	}
	if strings.Contains(hash, key) || validDashboardAccessKey(hash, key+"x") {
		t.Fatal("dashboard key hash must not contain or accept the raw key")
	}
	if _, _, err := generateDashboardAccessKey("too-short"); err == nil {
		t.Fatal("short custom dashboard key was accepted")
	}
}

func TestDashboardAccessKeyEncryptionRoundTrip(t *testing.T) {
	previousCryptKey := beego.AppConfig.String("auth_crypt_key")
	previousAuthKey := beego.AppConfig.String("auth_key")
	beego.AppConfig.Set("auth_crypt_key", "dashboard-test-crypt-key")
	beego.AppConfig.Set("auth_key", "dashboard-test-auth-key")
	t.Cleanup(func() {
		beego.AppConfig.Set("auth_crypt_key", previousCryptKey)
		beego.AppConfig.Set("auth_key", previousAuthKey)
	})

	key := dashboardAccessKeyPrefix + "encrypted-test-key-123456"
	ciphertext, err := encryptDashboardAccessKey(key)
	if err != nil {
		t.Fatalf("encryptDashboardAccessKey: %v", err)
	}
	if ciphertext == "" || strings.Contains(ciphertext, key) {
		t.Fatalf("encrypted dashboard key leaked plaintext: %q", ciphertext)
	}
	decrypted, err := decryptDashboardAccessKey(ciphertext)
	if err != nil {
		t.Fatalf("decryptDashboardAccessKey: %v", err)
	}
	if decrypted != key {
		t.Fatalf("decrypted dashboard key = %q, want %q", decrypted, key)
	}

	beego.AppConfig.Set("auth_crypt_key", "rotated-dashboard-test-key")
	if _, err := decryptDashboardAccessKey(ciphertext); err == nil {
		t.Fatal("dashboard key decrypted with a different encryption key")
	}
}

func newDashboardAccessController(t *testing.T, method string, action string, form url.Values, sessionValues map[interface{}]interface{}) (*DashboardAccessController, *httptest.ResponseRecorder) {
	t.Helper()
	request := httptest.NewRequest(method, "http://console.test/overview/accesskey", strings.NewReader(form.Encode()))
	request.Host = "console.test"
	request.Header.Set("Origin", "http://console.test")
	if len(form) > 0 {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	recorder := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(recorder, request)
	controller := &DashboardAccessController{}
	controller.Init(ctx, "DashboardAccessController", action, controller)
	session := &clientPermissionSession{values: sessionValues}
	controller.CruSession = session
	ctx.Input.CruSession = session
	return controller, recorder
}

func readDashboardAccessResponse(t *testing.T, recorder *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var response map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode dashboard access response %q: %v", recorder.Body.String(), err)
	}
	return response
}

func TestDashboardAccessStatusRestoresEncryptedCredential(t *testing.T) {
	db := useClientPermissionTestDb(t)
	previousWebBaseURL := beego.AppConfig.String("web_base_url")
	previousCryptKey := beego.AppConfig.String("auth_crypt_key")
	previousAuthKey := beego.AppConfig.String("auth_key")
	beego.AppConfig.Set("web_base_url", "http://console.test")
	beego.AppConfig.Set("auth_crypt_key", "dashboard-controller-crypt-key")
	beego.AppConfig.Set("auth_key", "dashboard-controller-auth-key")
	t.Cleanup(func() {
		beego.AppConfig.Set("web_base_url", previousWebBaseURL)
		beego.AppConfig.Set("auth_crypt_key", previousCryptKey)
		beego.AppConfig.Set("auth_key", previousAuthKey)
	})

	session := map[interface{}]interface{}{"auth": true, "isAdmin": true}
	generate, recorder := newDashboardAccessController(t, http.MethodPost, "Generate", url.Values{"key": {"persisted-dashboard-key-123456"}}, session)
	runClientPermissionAction(t, generate.Generate)
	generated := readDashboardAccessResponse(t, recorder)
	key, _ := generated["key"].(string)
	shortcut, _ := generated["url"].(string)
	if generated["status"] != float64(1) || key == "" || shortcut == "" {
		t.Fatalf("dashboard key generation response = %#v", generated)
	}

	status, recorder := newDashboardAccessController(t, http.MethodGet, "Status", nil, session)
	runClientPermissionAction(t, status.Status)
	restored := readDashboardAccessResponse(t, recorder)
	if restored["enabled"] != true || restored["key"] != key || restored["url"] != shortcut {
		t.Fatalf("dashboard key status did not restore encrypted credential = %#v", restored)
	}

	global := db.GetGlobal()
	global.RLock()
	ciphertext := global.DashboardKeyCiphertext
	global.RUnlock()
	if ciphertext == "" || strings.Contains(ciphertext, key) {
		t.Fatalf("dashboard key ciphertext leaked plaintext: %q", ciphertext)
	}
}

func TestLookupDashboardAccessKeyScopesAndRevokes(t *testing.T) {
	previous := file.GetDb()
	db := &file.DbUtils{JsonDb: file.NewJsonDb(t.TempDir())}
	file.Db = db
	t.Cleanup(func() { file.Db = previous })

	adminKey, adminHash, err := generateDashboardAccessKey("")
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	if err := db.SetGlobalDashboardKeyHash(adminHash); err != nil {
		t.Fatalf("store admin key: %v", err)
	}
	userKey, userHash, err := generateDashboardAccessKey("")
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	user := &file.User{Id: 7, UserName: "alice", Password: "secret", Status: true, DashboardKeyHash: userHash, ExpireTime: time.Now().Add(time.Hour).Format("2006-01-02 15:04:05")}
	db.JsonDb.Users.Store(user.Id, user)

	if principal, ok := lookupDashboardAccessKey(adminKey); !ok || !principal.Admin || principal.UserID != 0 {
		t.Fatalf("admin key lookup = %#v, %v", principal, ok)
	}
	if principal, ok := lookupDashboardAccessKey(userKey); !ok || principal.Admin || principal.UserID != user.Id {
		t.Fatalf("user key lookup = %#v, %v", principal, ok)
	}
	user.Lock()
	user.Status = false
	user.Unlock()
	if _, ok := lookupDashboardAccessKey(userKey); ok {
		t.Fatal("disabled user dashboard key remained valid")
	}
	user.Lock()
	user.Status = true
	user.ExpireTime = time.Now().Add(-time.Hour).Format("2006-01-02 15:04:05")
	user.Unlock()
	if _, ok := lookupDashboardAccessKey(userKey); ok {
		t.Fatal("expired user dashboard key remained valid")
	}
	if _, ok := lookupDashboardAccessKey(adminKey); !ok {
		t.Fatal("admin dashboard key should remain valid")
	}
}
