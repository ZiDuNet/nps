package controllers

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/astaxie/beego"
)

func withAPIV1RegistrationDefaults(t *testing.T, expireDays, maxClients, maxTunnels, maxConn, uploadLimit, downloadLimit int) {
	t.Helper()
	keys := []string{
		"register_expire_days", "register_max_clients", "register_max_tunnels",
		"register_max_conn", "register_upload_limit", "register_download_limit",
	}
	previous := make(map[string]string, len(keys))
	for _, key := range keys {
		previous[key] = beego.AppConfig.String(key)
	}
	_ = beego.AppConfig.Set("register_expire_days", strconv.Itoa(expireDays))
	_ = beego.AppConfig.Set("register_max_clients", strconv.Itoa(maxClients))
	_ = beego.AppConfig.Set("register_max_tunnels", strconv.Itoa(maxTunnels))
	_ = beego.AppConfig.Set("register_max_conn", strconv.Itoa(maxConn))
	_ = beego.AppConfig.Set("register_upload_limit", strconv.Itoa(uploadLimit))
	_ = beego.AppConfig.Set("register_download_limit", strconv.Itoa(downloadLimit))
	t.Cleanup(func() {
		for key, value := range previous {
			_ = beego.AppConfig.Set(key, value)
		}
	})
}

func TestAPIV1UsersRegisterUsesConfiguredDefaults(t *testing.T) {
	utils := useClientPermissionTestDb(t)
	withAPIV1TestAuthKey(t)
	withAPIV1RegistrationDefaults(t, 3, 2, 4, 6, 128, 256)

	before := time.Now()
	body := `{"username":"api-default","password":"secret12","email":"api-default@example.com"}`
	target := "/api/v1/users/register?" + apiV1SignedQuery().Encode()
	controller, recorder := newAPIV1TestController(t, http.MethodPost, target, strings.NewReader(body), "application/json")
	controller.Prepare()
	runClientPermissionAction(t, controller.UsersRegister)
	after := time.Now()

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	payload := readAPIV1JSON(t, recorder)
	if payload["status"] != float64(1) || payload["code"] != "ok" {
		t.Fatalf("registration envelope = %#v", payload)
	}
	data, ok := payload["data"].(map[string]interface{})
	if !ok || data["Id"] == nil {
		t.Fatalf("registration data = %#v", payload["data"])
	}
	user, err := utils.GetUser(int(data["Id"].(float64)))
	if err != nil {
		t.Fatalf("load registered user: %v", err)
	}
	user.RLock()
	expireTime, maxClients, maxTunnels := user.ExpireTime, user.MaxClientNum, user.MaxTunnelNum
	maxConn, uploadLimit, downloadLimit, email := user.MaxConn, user.UploadLimit, user.DownloadLimit, user.Email
	user.RUnlock()
	if maxClients != 2 || maxTunnels != 4 {
		t.Fatalf("configured quotas = %d clients, %d tunnels; want 2 and 4", maxClients, maxTunnels)
	}
	if maxConn != 6 || uploadLimit != 128 || downloadLimit != 256 {
		t.Fatalf("configured runtime limits = %d connections, %d/%d KB/s; want 6 and 128/256", maxConn, uploadLimit, downloadLimit)
	}
	if email != "api-default@example.com" {
		t.Fatalf("registered email = %q", email)
	}
	expiresAt, err := time.ParseInLocation("2006-01-02 15:04:05", expireTime, time.Local)
	if err != nil {
		t.Fatalf("parse registered expiry %q: %v", expireTime, err)
	}
	earliest := before.AddDate(0, 0, 3).Add(-time.Second)
	latest := after.AddDate(0, 0, 3).Add(time.Second)
	if expiresAt.Before(earliest) || expiresAt.After(latest) {
		t.Fatalf("registered expiry = %s, want approximately %s to %s", expiresAt, earliest, latest)
	}
}

func TestAPIV1UsersRegisterAcceptsExplicitPermanentExpiry(t *testing.T) {
	utils := useClientPermissionTestDb(t)
	withAPIV1TestAuthKey(t)
	withAPIV1RegistrationDefaults(t, 30, 1, 1, 8, 512, 1024)

	body := `{"username":"api-permanent","password":"secret12","expire_days":0,"max_client":0,"max_tunnel":0,"max_conn":0,"upload_limit":0,"download_limit":0}`
	target := "/api/v1/users/register?" + apiV1SignedQuery().Encode()
	controller, recorder := newAPIV1TestController(t, http.MethodPost, target, strings.NewReader(body), "application/json")
	controller.Prepare()
	runClientPermissionAction(t, controller.UsersRegister)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	payload := readAPIV1JSON(t, recorder)
	data, ok := payload["data"].(map[string]interface{})
	if !ok || data["Id"] == nil {
		t.Fatalf("registration data = %#v", payload["data"])
	}
	user, err := utils.GetUser(int(data["Id"].(float64)))
	if err != nil {
		t.Fatalf("load registered user: %v", err)
	}
	user.RLock()
	defer user.RUnlock()
	if user.ExpireTime != "" {
		t.Fatalf("explicit expire_days=0 produced expiry %q, want permanent", user.ExpireTime)
	}
	if user.MaxClientNum != 0 || user.MaxTunnelNum != 0 || user.MaxConn != 0 || user.UploadLimit != 0 || user.DownloadLimit != 0 {
		t.Fatalf("explicit zero limits = clients %d, tunnels %d, connections %d, bandwidth %d/%d; want unlimited",
			user.MaxClientNum, user.MaxTunnelNum, user.MaxConn, user.UploadLimit, user.DownloadLimit)
	}
}

func TestAPIV1UsersRegisterAcceptsRuntimeLimitAliases(t *testing.T) {
	utils := useClientPermissionTestDb(t)
	withAPIV1TestAuthKey(t)
	withAPIV1RegistrationDefaults(t, 0, 0, 0, 0, 0, 0)

	body := `{"username":"api-limits","password":"secret12","max_connection":7,"upload_rate":321,"download_rate":654}`
	target := "/api/v1/users/register?" + apiV1SignedQuery().Encode()
	controller, recorder := newAPIV1TestController(t, http.MethodPost, target, strings.NewReader(body), "application/json")
	controller.Prepare()
	runClientPermissionAction(t, controller.UsersRegister)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	payload := readAPIV1JSON(t, recorder)
	data, ok := payload["data"].(map[string]interface{})
	if !ok || data["Id"] == nil {
		t.Fatalf("registration data = %#v", payload["data"])
	}
	user, err := utils.GetUser(int(data["Id"].(float64)))
	if err != nil {
		t.Fatalf("load registered user: %v", err)
	}
	user.RLock()
	defer user.RUnlock()
	if user.MaxConn != 7 || user.UploadLimit != 321 || user.DownloadLimit != 654 {
		t.Fatalf("runtime limit aliases produced %d connections and %d/%d KB/s", user.MaxConn, user.UploadLimit, user.DownloadLimit)
	}
}

func TestAPIV1UsersRegisterRequiresAdministrator(t *testing.T) {
	controller, recorder := newAPIV1TestController(t, http.MethodPost, "/api/v1/users/register", nil, "")
	runClientPermissionAction(t, controller.UsersRegister)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
	payload := readAPIV1JSON(t, recorder)
	if payload["error_code"] != "forbidden" {
		t.Fatalf("permission envelope = %#v", payload)
	}
}

func TestRegistrationExpireDaysReadsConfiguration(t *testing.T) {
	withAPIV1RegistrationDefaults(t, 0, 0, 0, 0, 0, 0)
	if got := registrationExpireDays(); got != 0 {
		t.Fatalf("registrationExpireDays() = %d, want 0", got)
	}
	_ = beego.AppConfig.Set("register_expire_days", "12")
	if got := registrationExpireDays(); got != 12 {
		t.Fatalf("registrationExpireDays() = %d, want 12", got)
	}
}
