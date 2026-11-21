package controllers

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/astaxie/beego"
)

func withAPIV1RegistrationDefaults(t *testing.T, expireDays, maxClients, maxTunnels, maxHosts, maxConn int, bandwidthLimit string) {
	t.Helper()
	keys := []string{
		"register_expire_days", "register_max_clients", "register_max_tunnels", "register_max_hosts",
		"register_max_conn", "register_bandwidth_limit", "register_upload_limit", "register_download_limit",
	}
	previous := make(map[string]string, len(keys))
	for _, key := range keys {
		previous[key] = beego.AppConfig.String(key)
	}
	_ = beego.AppConfig.Set("register_expire_days", strconv.Itoa(expireDays))
	_ = beego.AppConfig.Set("register_max_clients", strconv.Itoa(maxClients))
	_ = beego.AppConfig.Set("register_max_tunnels", strconv.Itoa(maxTunnels))
	_ = beego.AppConfig.Set("register_max_hosts", strconv.Itoa(maxHosts))
	_ = beego.AppConfig.Set("register_max_conn", strconv.Itoa(maxConn))
	_ = beego.AppConfig.Set("register_bandwidth_limit", bandwidthLimit)
	_ = beego.AppConfig.Set("register_upload_limit", "0")
	_ = beego.AppConfig.Set("register_download_limit", "0")
	t.Cleanup(func() {
		for key, value := range previous {
			_ = beego.AppConfig.Set(key, value)
		}
	})
}

func TestAPIV1UsersRegisterUsesConfiguredDefaults(t *testing.T) {
	utils := useClientPermissionTestDb(t)
	withAPIV1TestAuthKey(t)
	withAPIV1RegistrationDefaults(t, 3, 2, 4, 5, 6, "384")

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
	expireTime, maxClients, maxTunnels, maxHosts := user.ExpireTime, user.MaxClientNum, user.MaxTunnelNum, user.MaxHostNum
	maxConn, bandwidthLimit, email := user.MaxConn, user.BandwidthLimit, user.Email
	user.RUnlock()
	if maxClients == nil || *maxClients != 2 || maxTunnels == nil || *maxTunnels != 4 || maxHosts == nil || *maxHosts != 5 {
		t.Fatalf("configured quotas = %v clients, %v tunnels, %v hosts; want 2, 4 and 5", maxClients, maxTunnels, maxHosts)
	}
	if maxConn == nil || *maxConn != 6 || bandwidthLimit == nil || *bandwidthLimit != 384_000_000 {
		t.Fatalf("configured runtime limits = %v connections, %v bps; want 6 and 384000000", maxConn, bandwidthLimit)
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
	withAPIV1RegistrationDefaults(t, 30, 1, 1, 1, 8, "")

	body := `{"username":"api-permanent","password":"secret12","expire_days":0,"max_client":0,"max_tunnel":0,"max_host":0,"max_conn":0}`
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
	if user.MaxClientNum == nil || *user.MaxClientNum != 0 ||
		user.MaxTunnelNum == nil || *user.MaxTunnelNum != 0 ||
		user.MaxHostNum == nil || *user.MaxHostNum != 0 ||
		user.MaxConn == nil || *user.MaxConn != 0 {
		t.Fatalf("explicit zero limits = clients %v, tunnels %v, hosts %v, connections %v; want not included",
			user.MaxClientNum, user.MaxTunnelNum, user.MaxHostNum, user.MaxConn)
	}
	if user.BandwidthLimit != nil {
		t.Fatalf("omitted bandwidth must use the unlimited default, got %v", *user.BandwidthLimit)
	}
}

func TestAPIV1UsersRegisterOmittedQuotasUseConfiguredDefaults(t *testing.T) {
	utils := useClientPermissionTestDb(t)
	withAPIV1TestAuthKey(t)
	withAPIV1RegistrationDefaults(t, 0, 2, 4, 5, 6, "384")

	body := `{"username":"api-omitted","password":"secret12"}`
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
		t.Fatal(err)
	}
	user.RLock()
	defer user.RUnlock()
	if user.MaxClientNum == nil || *user.MaxClientNum != 2 ||
		user.MaxTunnelNum == nil || *user.MaxTunnelNum != 4 ||
		user.MaxHostNum == nil || *user.MaxHostNum != 5 ||
		user.MaxConn == nil || *user.MaxConn != 6 ||
		user.BandwidthLimit == nil || *user.BandwidthLimit != 384_000_000 {
		t.Fatalf("omitted fields did not inherit configured defaults: clients %v, tunnels %v, hosts %v, connections %v, bandwidth %v",
			user.MaxClientNum, user.MaxTunnelNum, user.MaxHostNum, user.MaxConn, user.BandwidthLimit)
	}
}

func TestAPIV1UsersRegisterTreatsExplicitZeroAsNotIncluded(t *testing.T) {
	utils := useClientPermissionTestDb(t)
	withAPIV1TestAuthKey(t)
	withAPIV1RegistrationDefaults(t, 0, 0, 0, 0, 0, "")

	body := `{"username":"api-no-quota","password":"secret12","max_client":0,"max_tunnel":0,"max_host":0,"max_conn":0,"bandwidth_limit":0}`
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
	if user.MaxClientNum == nil || *user.MaxClientNum != 0 ||
		user.MaxTunnelNum == nil || *user.MaxTunnelNum != 0 ||
		user.MaxHostNum == nil || *user.MaxHostNum != 0 ||
		user.MaxConn == nil || *user.MaxConn != 0 ||
		user.BandwidthLimit == nil || *user.BandwidthLimit != 0 {
		t.Fatalf("explicit zeros = clients %v, tunnels %v, hosts %v, connections %v, bandwidth %v; want all not included",
			user.MaxClientNum, user.MaxTunnelNum, user.MaxHostNum, user.MaxConn, user.BandwidthLimit)
	}
}

func TestAPIV1UsersRegisterAcceptsRuntimeLimitAliases(t *testing.T) {
	utils := useClientPermissionTestDb(t)
	withAPIV1TestAuthKey(t)
	withAPIV1RegistrationDefaults(t, 0, 0, 0, 0, 0, "")

	body := `{"username":"api-limits","password":"secret12","max_connection":7,"upload_limit":321,"download_limit":654}`
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
	if user.MaxConn == nil || *user.MaxConn != 7 || user.BandwidthLimit == nil || *user.BandwidthLimit != (321+654)*8192 {
		t.Fatalf("runtime limit aliases produced %v connections and %v bps", user.MaxConn, user.BandwidthLimit)
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
	withAPIV1RegistrationDefaults(t, 0, 0, 0, 0, 0, "")
	if got := registrationExpireDays(); got != 0 {
		t.Fatalf("registrationExpireDays() = %d, want 0", got)
	}
	_ = beego.AppConfig.Set("register_expire_days", "12")
	if got := registrationExpireDays(); got != 12 {
		t.Fatalf("registrationExpireDays() = %d, want 12", got)
	}
}
