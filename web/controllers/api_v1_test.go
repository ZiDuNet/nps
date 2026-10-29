package controllers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ehang.io/nps/bridge"
	"ehang.io/nps/lib/connectionlog"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/ipreg"
	"ehang.io/nps/server"
	"github.com/astaxie/beego"
	beecontext "github.com/astaxie/beego/context"
)

const apiV1TestAuthKey = "api-v1-test-auth-key"

func withAPIV1TestAuthKey(t *testing.T) {
	t.Helper()
	previous := beego.AppConfig.String("auth_key")
	_ = beego.AppConfig.Set("auth_key", apiV1TestAuthKey)
	t.Cleanup(func() { _ = beego.AppConfig.Set("auth_key", previous) })
}

// withAPIV1TestBridge installs the minimal bridge state that list actions
// expect in production. Other controller tests rarely exercise those actions,
// so they do not need it.
func withAPIV1TestBridge(t *testing.T) {
	t.Helper()
	previous := server.Bridge
	if previous == nil {
		server.Bridge = &bridge.Bridge{TunnelPort: 8024}
		t.Cleanup(func() { server.Bridge = previous })
	}
}

func apiV1SignedQuery() url.Values {
	timestamp := time.Now().Unix()
	return url.Values{
		"auth_key":  {crypt.Md5(apiV1TestAuthKey + strconv.FormatInt(timestamp, 10))},
		"timestamp": {strconv.FormatInt(timestamp, 10)},
	}
}

func newAPIV1TestController(t *testing.T, method, target string, body io.Reader, contentType string) (*ApiV1Controller, *httptest.ResponseRecorder) {
	t.Helper()
	request := httptest.NewRequest(method, "http://console.test"+target, body)
	request.Host = "console.test"
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	recorder := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(recorder, request)
	session := &clientPermissionSession{values: map[interface{}]interface{}{}}
	ctx.Input.CruSession = session
	controller := &ApiV1Controller{}
	controller.Init(ctx, "ApiV1Controller", "Test", controller)
	return controller, recorder
}

func readAPIV1JSON(t *testing.T, recorder *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var payload map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode API response %q: %v", recorder.Body.String(), err)
	}
	return payload
}

func TestAPIV1PrepareRequiresCredentials(t *testing.T) {
	controller, recorder := newAPIV1TestController(t, http.MethodGet, "/api/v1/clients", nil, "")
	runClientPermissionAction(t, controller.Prepare)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	payload := readAPIV1JSON(t, recorder)
	if payload["status"] != float64(0) || payload["error_code"] != "unauthorized" {
		t.Fatalf("unauthorized envelope = %#v", payload)
	}
	if requestID, ok := payload["request_id"].(string); !ok || requestID == "" {
		t.Fatalf("unauthorized envelope is missing request_id: %#v", payload)
	}
}

func TestAPIV1PrepareRejectsInvalidSignatureWithoutRedirectLeak(t *testing.T) {
	withAPIV1TestAuthKey(t)
	query := url.Values{
		"auth_key":  {"not-the-right-signature"},
		"timestamp": {strconv.FormatInt(time.Now().Unix(), 10)},
	}
	controller, recorder := newAPIV1TestController(t, http.MethodGet, "/api/v1/clients?"+query.Encode(), nil, "")
	runClientPermissionAction(t, controller.Prepare)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	if location := recorder.Header().Get("Location"); location != "" {
		t.Fatalf("API response leaked a login redirect to %q", location)
	}
	payload := readAPIV1JSON(t, recorder)
	if payload["status"] != float64(0) || payload["error_code"] != "unauthorized" {
		t.Fatalf("invalid credential envelope = %#v", payload)
	}
}

func TestAPIV1AdminSignatureListsClients(t *testing.T) {
	useClientPermissionTestDb(t)
	withAPIV1TestAuthKey(t)
	withAPIV1TestBridge(t)

	controller, recorder := newAPIV1TestController(t, http.MethodGet, "/api/v1/clients?"+apiV1SignedQuery().Encode(), nil, "")
	controller.Prepare()
	runClientPermissionAction(t, controller.Clients)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	payload := readAPIV1JSON(t, recorder)
	if payload["status"] != float64(1) || payload["code"] != "ok" {
		t.Fatalf("list envelope = %#v", payload)
	}
	if _, ok := payload["rows"].([]interface{}); !ok {
		t.Fatalf("list rows = %#v, want array", payload["rows"])
	}
	if payload["total"] != float64(0) {
		t.Fatalf("list total = %#v, want 0", payload["total"])
	}
}

func TestAPIV1UserTokenIsScopedToOwnedClients(t *testing.T) {
	db := useClientPermissionTestDb(t)
	withAPIV1TestBridge(t)
	token, hash, err := generateUserAPIToken()
	if err != nil {
		t.Fatalf("generateUserAPIToken: %v", err)
	}
	user := &file.User{
		Id: 7, UserName: "alice", Status: true,
		ExpireTime: time.Now().Add(24 * time.Hour).Format("2006-01-02 15:04:05"),
		APIKeyHash: hash,
	}
	db.JsonDb.Users.Store(user.Id, user)
	db.JsonDb.Clients.Store(1, &file.Client{Id: 1, VerifyKey: "owned-vkey", UserId: 7, Remark: "owned", Status: true})
	db.JsonDb.Clients.Store(2, &file.Client{Id: 2, VerifyKey: "other-vkey", UserId: 8, Remark: "other", Status: true})

	controller, recorder := newAPIV1TestController(t, http.MethodGet, "/api/v1/clients", nil, "")
	controller.Ctx.Request.Header.Set("Authorization", "Bearer "+token)
	controller.Prepare()
	if !controller.userAPIAuthorized || controller.apiUserID != 7 {
		t.Fatalf("user principal = (%v, %d), want authenticated user 7", controller.userAPIAuthorized, controller.apiUserID)
	}
	runClientPermissionAction(t, controller.Clients)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	payload := readAPIV1JSON(t, recorder)
	rows, ok := payload["rows"].([]interface{})
	if !ok || len(rows) != 1 {
		t.Fatalf("user rows = %#v, want exactly one owned client", payload["rows"])
	}
	row, _ := rows[0].(map[string]interface{})
	if row["Id"] != float64(1) {
		t.Fatalf("user saw client %#v, want only client 1", row["Id"])
	}
}

func TestAPIV1UserTokenCannotReadAnotherUsersClient(t *testing.T) {
	db := useClientPermissionTestDb(t)
	withAPIV1TestBridge(t)
	token, hash, err := generateUserAPIToken()
	if err != nil {
		t.Fatalf("generateUserAPIToken: %v", err)
	}
	db.JsonDb.Users.Store(7, &file.User{
		Id: 7, UserName: "alice", Status: true,
		ExpireTime: time.Now().Add(24 * time.Hour).Format("2006-01-02 15:04:05"),
		APIKeyHash: hash,
	})
	db.JsonDb.Clients.Store(1, &file.Client{Id: 1, VerifyKey: "owned-vkey", UserId: 7, Status: true})
	db.JsonDb.Clients.Store(2, &file.Client{Id: 2, VerifyKey: "other-vkey", UserId: 8, Status: true})

	controller, recorder := newAPIV1TestController(t, http.MethodGet, "/api/v1/clients/2", nil, "")
	controller.Ctx.Request.Header.Set("Authorization", "Bearer "+token)
	controller.Prepare()
	controller.Ctx.Input.SetParam(":id", "2")
	runClientPermissionAction(t, controller.ClientGet)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
	payload := readAPIV1JSON(t, recorder)
	if payload["error_code"] != "forbidden" {
		t.Fatalf("foreign client envelope = %#v", payload)
	}
}

func TestAPIV1AuditConnectionsScopesUserTokenToOwnedClients(t *testing.T) {
	db := useClientPermissionTestDb(t)
	token, hash, err := generateUserAPIToken()
	if err != nil {
		t.Fatalf("generateUserAPIToken: %v", err)
	}
	db.JsonDb.Users.Store(7, &file.User{
		Id: 7, UserName: "alice", Status: true,
		ExpireTime: time.Now().Add(24 * time.Hour).Format("2006-01-02 15:04:05"),
		APIKeyHash: hash,
	})
	db.JsonDb.Clients.Store(101, &file.Client{Id: 101, UserId: 7, Remark: "visible", Status: true})
	db.JsonDb.Clients.Store(102, &file.Client{Id: 102, UserId: 9, Remark: "hidden", Status: true})

	logPath := filepath.Join(t.TempDir(), "connection.log")
	previousLogPath := beego.AppConfig.String("connection_log_path")
	_ = beego.AppConfig.Set("connection_log_path", logPath)
	connectionlog.Configure(logPath)
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("connection_log_path", previousLogPath)
		connectionlog.Configure("")
	})
	for _, event := range []connectionlog.Event{
		{ClientID: 101, UserID: 7, ClientRemark: "visible", Action: connectionlog.ActionBridgeConnected},
		{ClientID: 102, UserID: 9, ClientRemark: "hidden", Action: connectionlog.ActionBridgeConnected},
	} {
		if err := connectionlog.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	connectionlog.Flush()

	controller, recorder := newAPIV1TestController(t, http.MethodGet, "/api/v1/audit/connections", nil, "")
	controller.Ctx.Request.Header.Set("Authorization", "Bearer "+token)
	controller.Prepare()
	runClientPermissionAction(t, controller.AuditConnections)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	payload := readAPIV1JSON(t, recorder)
	rows, ok := payload["rows"].([]interface{})
	if !ok || len(rows) != 1 {
		t.Fatalf("user connection rows = %#v, want one owned record", payload["rows"])
	}
	row, _ := rows[0].(map[string]interface{})
	if row["Client"] != "visible #101" {
		t.Fatalf("visible connection row = %#v", row)
	}
}

func TestAPIV1JSONBodyMergesIntoLegacyForm(t *testing.T) {
	body := `{"remark":"json client","vkey":"json-vkey","flow_limit":123,"compress":true,"allow_ports":["80","443"]}`
	controller, _ := newAPIV1TestController(t, http.MethodPost, "/api/v1/clients", strings.NewReader(body), "application/json")
	if err := controller.mergeJSONBody(); err != nil {
		t.Fatalf("mergeJSONBody: %v", err)
	}
	if got := controller.GetString("remark"); got != "json client" {
		t.Fatalf("remark = %q", got)
	}
	if got := controller.GetIntNoErr("flow_limit"); got != 123 {
		t.Fatalf("flow_limit = %d, want 123", got)
	}
	if got := controller.GetString("compress"); got != "true" {
		t.Fatalf("compress = %q, want true", got)
	}
	if got := controller.GetString("allow_ports"); got != "80,443" {
		t.Fatalf("allow_ports = %q, want 80,443", got)
	}
}

func newAPIV1InfoTestController(t *testing.T, method, target string) (*ApiV1InfoController, *httptest.ResponseRecorder) {
	t.Helper()
	return newAPIV1InfoTestControllerWithBody(t, method, target, nil, "")
}

func newAPIV1InfoTestControllerWithBody(t *testing.T, method, target string, body io.Reader, contentType string) (*ApiV1InfoController, *httptest.ResponseRecorder) {
	t.Helper()
	request := httptest.NewRequest(method, "http://console.test"+target, body)
	request.Host = "console.test"
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	recorder := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(recorder, request)
	controller := &ApiV1InfoController{}
	controller.Init(ctx, "ApiV1InfoController", "Test", controller)
	return controller, recorder
}

func TestAPIV1PublicTimeEnvelope(t *testing.T) {
	controller, recorder := newAPIV1InfoTestController(t, http.MethodGet, "/api/v1/time")
	runClientPermissionAction(t, controller.Time)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if cacheControl := recorder.Header().Get("Cache-Control"); !strings.Contains(cacheControl, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", cacheControl)
	}
	payload := readAPIV1JSON(t, recorder)
	if payload["status"] != float64(1) || payload["code"] != "ok" {
		t.Fatalf("time envelope = %#v", payload)
	}
	data, ok := payload["data"].(map[string]interface{})
	if !ok || data["time"] == nil {
		t.Fatalf("time data = %#v", payload["data"])
	}
	if requestID, ok := payload["request_id"].(string); !ok || requestID == "" {
		t.Fatalf("time response is missing request_id: %#v", payload)
	}
}

func TestAPIV1InfoDiscoveryIsPublic(t *testing.T) {
	controller, recorder := newAPIV1InfoTestController(t, http.MethodGet, "/api/v1")
	runClientPermissionAction(t, controller.Info)

	payload := readAPIV1JSON(t, recorder)
	if payload["status"] != float64(1) || payload["version"] == "" {
		t.Fatalf("discovery payload = %#v", payload)
	}
	resources, ok := payload["resources"].([]interface{})
	if !ok || len(resources) == 0 {
		t.Fatalf("discovery resources = %#v", payload["resources"])
	}
}

func TestAPIV1IPRegisterRejectsNonPostWithEnvelope(t *testing.T) {
	controller, recorder := newAPIV1InfoTestController(t, http.MethodGet, "/api/v1/ip-register")
	runClientPermissionAction(t, controller.IPRegister)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
	payload := readAPIV1JSON(t, recorder)
	if payload["status"] != float64(0) || payload["error_code"] != "method_not_allowed" {
		t.Fatalf("IP registration method envelope = %#v", payload)
	}
}

func TestAPIV1IPRegisterUsesRequestBodyAndPeerAddress(t *testing.T) {
	db := useClientPermissionTestDb(t)
	db.JsonDb.Clients.Store(23, &file.Client{
		Id:               23,
		VerifyKey:        "client-register-key",
		RegisterMaxHours: 3,
		Status:           true,
	})
	oldAllow := beego.AppConfig.String("allow_x_real_ip")
	_ = beego.AppConfig.Set("allow_x_real_ip", "false")
	ipreg.Reset()
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("allow_x_real_ip", oldAllow)
		ipreg.Reset()
	})

	controller, recorder := newAPIV1InfoTestControllerWithBody(
		t,
		http.MethodPost,
		"/api/v1/ip-register",
		strings.NewReader(`{"key":"client-register-key","hours":2}`),
		"application/json",
	)
	controller.Ctx.Request.RemoteAddr = "198.51.100.23:43210"
	controller.Ctx.Request.Header.Set("X-Real-IP", "203.0.113.90")
	runClientPermissionAction(t, controller.IPRegister)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	payload := readAPIV1JSON(t, recorder)
	data, ok := payload["data"].(map[string]interface{})
	if payload["status"] != float64(1) || !ok || data["ip"] != "198.51.100.23" {
		t.Fatalf("IP registration envelope = %#v", payload)
	}
	if !ipreg.RegisteredString(23, "198.51.100.23:8080") {
		t.Fatal("API registration did not create the client-scoped runtime entry")
	}
	if ipreg.RegisteredString(23, "203.0.113.90:8080") {
		t.Fatal("API registration trusted X-Real-IP while allow_x_real_ip was disabled")
	}
}

func TestAPIV1IPRegisterAcceptsPostForm(t *testing.T) {
	db := useClientPermissionTestDb(t)
	db.JsonDb.Clients.Store(23, &file.Client{
		Id:               23,
		VerifyKey:        "client-register-key",
		RegisterMaxHours: 3,
		Status:           true,
	})
	oldAllow := beego.AppConfig.String("allow_x_real_ip")
	_ = beego.AppConfig.Set("allow_x_real_ip", "false")
	ipreg.Reset()
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("allow_x_real_ip", oldAllow)
		ipreg.Reset()
	})

	controller, recorder := newAPIV1InfoTestControllerWithBody(
		t,
		http.MethodPost,
		"/api/v1/ip-register",
		strings.NewReader("key=client-register-key&hours=2"),
		"application/x-www-form-urlencoded",
	)
	controller.Ctx.Request.RemoteAddr = "198.51.100.24:43210"
	runClientPermissionAction(t, controller.IPRegister)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if !ipreg.RegisteredString(23, "198.51.100.24:8080") {
		t.Fatal("form registration did not create the client-scoped runtime entry")
	}
}

func TestAPIV1IPRegisterRejectsQueryStringCredential(t *testing.T) {
	db := useClientPermissionTestDb(t)
	db.JsonDb.Clients.Store(23, &file.Client{
		Id:               23,
		VerifyKey:        "client-register-key",
		RegisterMaxHours: 3,
		Status:           true,
	})
	ipreg.Reset()
	t.Cleanup(ipreg.Reset)

	controller, recorder := newAPIV1InfoTestController(
		t,
		http.MethodPost,
		"/api/v1/ip-register?key=client-register-key&hours=2",
	)
	controller.Ctx.Request.RemoteAddr = "198.51.100.25:43210"
	runClientPermissionAction(t, controller.IPRegister)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
	}
	payload := readAPIV1JSON(t, recorder)
	if payload["error_code"] != "invalid_registration_key" {
		t.Fatalf("query-string credential response = %#v", payload)
	}
	if ipreg.RegisteredString(23, "198.51.100.25:8080") {
		t.Fatal("query-string credential unexpectedly registered the peer IP")
	}
}

func TestAPIV1LegacyFailureBecomesErrorEnvelope(t *testing.T) {
	useClientPermissionTestDb(t)
	withAPIV1TestAuthKey(t)

	controller, recorder := newAPIV1TestController(t, http.MethodGet, "/api/v1/clients/999?"+apiV1SignedQuery().Encode(), nil, "")
	controller.Prepare()
	controller.Ctx.Input.SetParam(":id", "999")
	runClientPermissionAction(t, controller.ClientGet)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusNotFound, recorder.Body.String())
	}
	payload := readAPIV1JSON(t, recorder)
	if payload["status"] != float64(0) || payload["code"] != "error" || payload["error_code"] != "operation_failed" {
		t.Fatalf("failure envelope = %#v", payload)
	}
}
