package controllers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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
	"ehang.io/nps/lib/settings"
	"ehang.io/nps/server"
	"ehang.io/nps/web/audit"
	"github.com/astaxie/beego"
	beecontext "github.com/astaxie/beego/context"
)

const apiV1TestAuthKey = "api-v1-test-auth-key"

func withAPIV1TestAuthKey(t *testing.T) {
	t.Helper()
	previous := beego.AppConfig.String("auth_key")
	previousLegacy := beego.AppConfig.DefaultBool("api_legacy_md5_enabled", false)
	_ = beego.AppConfig.Set("auth_key", apiV1TestAuthKey)
	_ = beego.AppConfig.Set("api_legacy_md5_enabled", "true")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("auth_key", previous)
		_ = beego.AppConfig.Set("api_legacy_md5_enabled", strconv.FormatBool(previousLegacy))
	})
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

func TestAPIV1BrowserAdminSessionCannotAuthorizeOrBeMutatedByInvalidCredentials(t *testing.T) {
	for _, test := range []struct {
		name  string
		token string
	}{
		{name: "missing credentials"},
		{name: "invalid bearer", token: "not-a-valid-token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller, recorder := newAPIV1TestController(t, http.MethodGet, "/api/v1/clients", nil, "")
			session := controller.Ctx.Input.CruSession.(*clientPermissionSession)
			session.values["auth"] = true
			session.values["isAdmin"] = true
			session.values["username"] = "browser-admin"
			if test.token != "" {
				controller.Ctx.Request.Header.Set("Authorization", "Bearer "+test.token)
			}

			runClientPermissionAction(t, controller.Prepare)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
			}
			if controller.apiAuthorized || controller.userAPIAuthorized || controller.IsAdmin() {
				t.Fatal("browser administrator session authorized an API-only request")
			}
			if got := session.values["username"]; got != "browser-admin" || session.values["auth"] != true || session.values["isAdmin"] != true {
				t.Fatalf("browser session was mutated by rejected API request: %#v", session.values)
			}
			if cookies := recorder.Result().Cookies(); len(cookies) != 0 {
				t.Fatalf("rejected API request changed browser cookies: %#v", cookies)
			}
		})
	}
}

func TestAPIV1UserTokenDoesNotInheritBrowserAdministratorSession(t *testing.T) {
	db := useClientPermissionTestDb(t)
	token, hash, err := generateUserAPIToken()
	if err != nil {
		t.Fatal(err)
	}
	db.JsonDb.Users.Store(7, &file.User{Id: 7, UserName: "alice", Status: true, APIKeyHash: hash})

	controller, recorder := newAPIV1TestController(t, http.MethodGet, "/api/v1/clients", nil, "")
	session := controller.Ctx.Input.CruSession.(*clientPermissionSession)
	session.values["auth"] = true
	session.values["isAdmin"] = true
	session.values["username"] = "browser-admin"
	controller.Ctx.Request.Header.Set("Authorization", "Bearer "+token)
	controller.Prepare()

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if !controller.userAPIAuthorized || controller.apiUserID != 7 || controller.IsAdmin() {
		t.Fatalf("API principal = (user=%v, id=%d, admin=%v), want user 7 without admin privilege", controller.userAPIAuthorized, controller.apiUserID, controller.IsAdmin())
	}
	if got := session.values["username"]; got != "browser-admin" || session.values["isAdmin"] != true {
		t.Fatalf("user API request changed unrelated browser session: %#v", session.values)
	}
}

func TestAPIV1HMACSignatureAllowsRequestAndRejectsReplay(t *testing.T) {
	useClientPermissionTestDb(t)
	previousKey := beego.AppConfig.String("auth_key")
	previousHMAC := beego.AppConfig.DefaultBool("api_hmac_enabled", true)
	previousLegacy := beego.AppConfig.DefaultBool("api_legacy_md5_enabled", false)
	_ = beego.AppConfig.Set("auth_key", apiV1TestAuthKey)
	_ = beego.AppConfig.Set("api_hmac_enabled", "true")
	_ = beego.AppConfig.Set("api_legacy_md5_enabled", "false")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("auth_key", previousKey)
		_ = beego.AppConfig.Set("api_hmac_enabled", strconv.FormatBool(previousHMAC))
		_ = beego.AppConfig.Set("api_legacy_md5_enabled", strconv.FormatBool(previousLegacy))
	})

	body := []byte(`{"limit":10}`)
	controller, recorder := newAPIV1TestController(t, http.MethodPost, "/api/v1/clients?tag=a%20b", bytes.NewReader(body), "application/json")
	request := controller.Ctx.Request
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce, err := newNPSHMACNonce()
	if err != nil {
		t.Fatal(err)
	}
	bodySum := sha256.Sum256(body)
	bodyHash := hex.EncodeToString(bodySum[:])
	keyID := npsHMACAuthKeyID
	signature, err := signNPSHMAC(apiV1TestAuthKey, request, bodyHash, timestamp, nonce, keyID)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", npsHMACAuthorizationScheme+" "+signature)
	request.Header.Set("X-NPS-Timestamp", timestamp)
	request.Header.Set("X-NPS-Nonce", nonce)
	request.Header.Set("X-NPS-Key-Id", keyID)
	controller.Prepare()
	if recorder.Code != http.StatusOK {
		t.Fatalf("valid HMAC status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if !controller.IsAdmin() {
		t.Fatal("verified API HMAC principal is not recognized as administrator")
	}

	replay, replayRecorder := newAPIV1TestController(t, http.MethodPost, "/api/v1/clients?tag=a%20b", bytes.NewReader(body), "application/json")
	replayRequest := replay.Ctx.Request
	replaySignature, err := signNPSHMAC(apiV1TestAuthKey, replayRequest, bodyHash, timestamp, nonce, keyID)
	if err != nil {
		t.Fatal(err)
	}
	replayRequest.Header.Set("Authorization", npsHMACAuthorizationScheme+" "+replaySignature)
	replayRequest.Header.Set("X-NPS-Timestamp", timestamp)
	replayRequest.Header.Set("X-NPS-Nonce", nonce)
	replayRequest.Header.Set("X-NPS-Key-Id", keyID)
	runClientPermissionAction(t, replay.Prepare)
	if replayRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("replayed HMAC status = %d, want %d: %s", replayRecorder.Code, http.StatusUnauthorized, replayRecorder.Body.String())
	}
}

func TestAPIV1HMACDelegatesAdminPrincipalToResourceController(t *testing.T) {
	useClientPermissionTestDb(t)
	withAPIV1TestBridge(t)
	previousKey := beego.AppConfig.String("auth_key")
	previousHMAC := beego.AppConfig.DefaultBool("api_hmac_enabled", true)
	previousLegacy := beego.AppConfig.DefaultBool("api_legacy_md5_enabled", false)
	_ = beego.AppConfig.Set("auth_key", apiV1TestAuthKey)
	_ = beego.AppConfig.Set("api_hmac_enabled", "true")
	_ = beego.AppConfig.Set("api_legacy_md5_enabled", "false")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("auth_key", previousKey)
		_ = beego.AppConfig.Set("api_hmac_enabled", strconv.FormatBool(previousHMAC))
		_ = beego.AppConfig.Set("api_legacy_md5_enabled", strconv.FormatBool(previousLegacy))
	})

	controller, recorder := newAPIV1TestController(t, http.MethodGet, "/api/v1/clients", nil, "")
	request := controller.Ctx.Request
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce, err := newNPSHMACNonce()
	if err != nil {
		t.Fatal(err)
	}
	keyID := npsHMACAuthKeyID
	signature, err := signNPSHMAC(apiV1TestAuthKey, request, hashEmptyBody(), timestamp, nonce, keyID)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", npsHMACAuthorizationScheme+" "+signature)
	request.Header.Set("X-NPS-Timestamp", timestamp)
	request.Header.Set("X-NPS-Nonce", nonce)
	request.Header.Set("X-NPS-Key-Id", keyID)

	controller.Prepare()
	if !controller.apiAuthorized || !controller.apiPrincipalSet {
		t.Fatalf("outer API principal = (%v, %v), want verified administrator", controller.apiAuthorized, controller.apiPrincipalSet)
	}
	runClientPermissionAction(t, controller.Clients)
	if recorder.Code != http.StatusOK {
		t.Fatalf("delegated HMAC resource status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	payload := readAPIV1JSON(t, recorder)
	if payload["status"] != float64(1) || payload["code"] != "ok" {
		t.Fatalf("delegated HMAC resource response = %#v, want success", payload)
	}
}

func TestAPIV1HMACRejectsModifiedSignedRequest(t *testing.T) {
	previousKey := beego.AppConfig.String("auth_key")
	previousHMAC := beego.AppConfig.DefaultBool("api_hmac_enabled", true)
	previousLegacy := beego.AppConfig.DefaultBool("api_legacy_md5_enabled", false)
	_ = beego.AppConfig.Set("auth_key", apiV1TestAuthKey)
	_ = beego.AppConfig.Set("api_hmac_enabled", "true")
	_ = beego.AppConfig.Set("api_legacy_md5_enabled", "false")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("auth_key", previousKey)
		_ = beego.AppConfig.Set("api_hmac_enabled", strconv.FormatBool(previousHMAC))
		_ = beego.AppConfig.Set("api_legacy_md5_enabled", strconv.FormatBool(previousLegacy))
	})

	for _, test := range []struct {
		name   string
		target string
		mutate func(*http.Request)
	}{
		{
			name: "body",
			mutate: func(request *http.Request) {
				request.Body = io.NopCloser(strings.NewReader(`{"limit":11}`))
			},
		},
		{
			name: "query",
			mutate: func(request *http.Request) {
				request.URL.RawQuery = "tag=changed"
			},
		},
		{
			name:   "api_token query credential",
			target: "/api/v1/clients?api_token=original",
			mutate: func(request *http.Request) {
				query := request.URL.Query()
				query.Set("api_token", "changed")
				request.URL.RawQuery = query.Encode()
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"limit":10}`)
			target := test.target
			if target == "" {
				target = "/api/v1/clients?tag=original"
			}
			controller, recorder := newAPIV1TestController(t, http.MethodPost, target, bytes.NewReader(body), "application/json")
			request := controller.Ctx.Request
			timestamp := strconv.FormatInt(time.Now().Unix(), 10)
			nonce, err := newNPSHMACNonce()
			if err != nil {
				t.Fatal(err)
			}
			bodySum := sha256.Sum256(body)
			keyID := npsHMACAuthKeyID
			signature, err := signNPSHMAC(apiV1TestAuthKey, request, hex.EncodeToString(bodySum[:]), timestamp, nonce, keyID)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(request)
			request.Header.Set("Authorization", npsHMACAuthorizationScheme+" "+signature)
			request.Header.Set("X-NPS-Timestamp", timestamp)
			request.Header.Set("X-NPS-Nonce", nonce)
			request.Header.Set("X-NPS-Key-Id", keyID)

			runClientPermissionAction(t, controller.Prepare)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("modified request status = %d, want %d: %s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
			}
		})
	}
}

func TestNPSHMACCanonicalRequestUsesGoEscapedPathAndCanonicalQuery(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		wantPath  string
		wantQuery string
	}{
		{
			name:      "duplicate and empty query values",
			target:    "/api/v1/items?z=&tag=one&tag=two&auth_key=business&timestamp=123",
			wantPath:  "/api/v1/items",
			wantQuery: "auth_key=business&tag=one&tag=two&timestamp=123&z=",
		},
		{
			name:      "space plus and percent query encoding",
			target:    "/api/v1/items?q=a+b&q=a%2Bb&literal=%25&space=a%20b",
			wantPath:  "/api/v1/items",
			wantQuery: "literal=%25&q=a+b&q=a%2Bb&space=a+b",
		},
		{
			name:      "escaped and unicode path",
			target:    "/api/v1/caf%C3%A9%20room/~!*'()%5B%5D?x=%E4%B8%AD%E6%96%87",
			wantPath:  "/api/v1/caf%C3%A9%20room/~!*'()%5B%5D",
			wantQuery: "x=%E4%B8%AD%E6%96%87",
		},
		{
			name:      "semicolon remains an encoded value",
			target:    "/api/v1/items?q=a%3Bb",
			wantPath:  "/api/v1/items",
			wantQuery: "q=a%3Bb",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.target, nil)
			canonical, err := npsHMACCanonicalRequest(request, hashEmptyBody(), "123", "nonce", npsHMACAuthKeyID)
			if err != nil {
				t.Fatal(err)
			}
			parts := strings.Split(canonical, "\n")
			if len(parts) != 7 {
				t.Fatalf("canonical fields = %d, want 7: %q", len(parts), canonical)
			}
			if parts[1] != test.wantPath {
				t.Errorf("escaped path = %q, want %q", parts[1], test.wantPath)
			}
			if parts[2] != test.wantQuery {
				t.Errorf("canonical query = %q, want %q", parts[2], test.wantQuery)
			}
		})
	}
}

func TestNPSHMACCanonicalRequestRejectsMalformedQuery(t *testing.T) {
	for _, target := range []string{"/api/v1/items?x=%ZZ", "/api/v1/items?x=a;b"} {
		t.Run(target, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, target, nil)
			if _, err := npsHMACCanonicalRequest(request, hashEmptyBody(), "123", "nonce", npsHMACAuthKeyID); err == nil {
				t.Fatalf("malformed query %q was accepted", target)
			}
		})
	}
}

func TestAPIV1LegacyMD5DisabledByDefault(t *testing.T) {
	withAPIV1TestAuthKey(t)
	_ = beego.AppConfig.Set("api_legacy_md5_enabled", "false")
	controller, recorder := newAPIV1TestController(t, http.MethodGet, "/api/v1/clients?"+apiV1SignedQuery().Encode(), nil, "")
	runClientPermissionAction(t, controller.Prepare)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("legacy MD5 status = %d, want %d: %s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
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

func TestAPIV1UserGetReadsPathID(t *testing.T) {
	db := useClientPermissionTestDb(t)
	withAPIV1TestAuthKey(t)
	db.JsonDb.Users.Store(901, &file.User{
		Id:       901,
		UserName: "path-user",
		Password: "secret",
		Status:   true,
	})

	controller, recorder := newAPIV1TestController(t, http.MethodGet, "/api/v1/users/901?"+apiV1SignedQuery().Encode(), nil, "")
	controller.Ctx.Input.SetParam(":id", "901")
	controller.Prepare()
	runClientPermissionAction(t, controller.UserGet)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	payload := readAPIV1JSON(t, recorder)
	data, ok := payload["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("user payload = %#v, want object in data", payload["data"])
	}
	if data["Id"] != float64(901) || data["UserName"] != "path-user" {
		t.Fatalf("user payload = %#v, want id 901 / path-user", data)
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
	if payload["log_enabled"] != true {
		t.Fatal("API omitted connection journal status")
	}
	if _, exists := payload["log_path"]; exists {
		t.Fatal("ordinary user API received server filesystem path")
	}
	if _, exists := payload["dropped"]; exists {
		t.Fatal("ordinary user API received global queue diagnostics")
	}
}

func TestAPIV1AuditMetadataPreservesRawCodesAndScopesDiagnostics(t *testing.T) {
	db := useClientPermissionTestDb(t)
	withAPIV1TestAuthKey(t)
	token, hash, err := generateUserAPIToken()
	if err != nil {
		t.Fatal(err)
	}
	db.JsonDb.Users.Store(7, &file.User{Id: 7, UserName: "alice", Status: true, APIKeyHash: hash})
	logPath := filepath.Join(t.TempDir(), "audit.log")
	previous := beego.AppConfig.String("audit_log_path")
	_ = beego.AppConfig.Set("audit_log_path", logPath)
	audit.Configure(logPath)
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("audit_log_path", previous)
		audit.Configure("")
	})
	for _, owner := range []int{0, 7, 9} {
		if err := audit.Append(audit.Event{Action: "auth.login", OwnerUserID: owner, RequestID: "request-safe", AuthMethod: "password"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, admin := range []bool{false, true} {
		t.Run(strconv.FormatBool(admin), func(t *testing.T) {
			target := "/api/v1/audit"
			if admin {
				target += "?" + apiV1SignedQuery().Encode()
			}
			controller, recorder := newAPIV1TestController(t, http.MethodGet, target, nil, "")
			if !admin {
				controller.Ctx.Request.Header.Set("Authorization", "Bearer "+token)
			}
			controller.Prepare()
			runClientPermissionAction(t, controller.Audit)
			payload := readAPIV1JSON(t, recorder)
			rows, ok := payload["rows"].([]interface{})
			want := 1
			if admin {
				want = 3
			}
			if !ok || len(rows) != want || payload["log_enabled"] != true {
				t.Fatalf("audit API response: %#v", payload)
			}
			row := rows[0].(map[string]interface{})
			if row["Action"] != "auth.login" || row["ActionLabel"] != "登录" || row["RequestID"] != "request-safe" {
				t.Fatalf("audit API fields: %#v", row)
			}
			if path, exists := payload["log_path"]; exists != admin || (admin && path != logPath) {
				t.Fatalf("audit API path visibility: %#v", payload)
			}
		})
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
	auth, ok := payload["auth"].([]interface{})
	if !ok {
		t.Fatalf("discovery auth methods = %#v, want array", payload["auth"])
	}
	if !containsString(auth, "admin_hmac_sha256") || !containsString(auth, "user_bearer_token") || containsString(auth, "admin_md5_timestamp") {
		t.Fatalf("default discovery auth methods = %#v, want HMAC and user bearer only", auth)
	}
}

func TestAPIV1InfoDiscoveryReportsEnabledLegacyMD5Only(t *testing.T) {
	previousHMAC := beego.AppConfig.DefaultBool("api_hmac_enabled", true)
	previousLegacy := beego.AppConfig.DefaultBool("api_legacy_md5_enabled", false)
	_ = beego.AppConfig.Set("api_hmac_enabled", "false")
	_ = beego.AppConfig.Set("api_legacy_md5_enabled", "true")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("api_hmac_enabled", strconv.FormatBool(previousHMAC))
		_ = beego.AppConfig.Set("api_legacy_md5_enabled", strconv.FormatBool(previousLegacy))
	})

	controller, recorder := newAPIV1InfoTestController(t, http.MethodGet, "/api/v1")
	runClientPermissionAction(t, controller.Info)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	auth := readAPIV1JSON(t, recorder)["auth"].([]interface{})
	if containsString(auth, "admin_hmac_sha256") || !containsString(auth, "admin_legacy_md5_timestamp") || !containsString(auth, "user_bearer_token") {
		t.Fatalf("configured discovery auth methods = %#v", auth)
	}
}

func containsString(values []interface{}, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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

func TestAPIV1GlobalConfigReadsSQLiteBusinessSettings(t *testing.T) {
	withAPIV1TestAuthKey(t)
	withSettingsFixture(t, "web_port=8081\nallow_user_register=false\n"+
		"api_hmac_enabled=true\napi_hmac_time_window=60\napi_legacy_md5_enabled=true\n")
	settingsBefore := settings.Snapshot()
	if err := settings.Save(map[string]string{
		"allow_user_register":      "true",
		"register_max_clients":     "3",
		"register_max_tunnels":     "0",
		"register_max_hosts":       "",
		"register_bandwidth_limit": "384",
	}, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		values := make(map[string]string, len(settingsBefore))
		for key, value := range settingsBefore {
			values[key] = value
		}
		if err := settings.Save(values, nil); err != nil {
			t.Fatalf("restore settings fixture: %v", err)
		}
	})

	controller, recorder := newAPIV1TestController(t, http.MethodGet, "/api/v1/global/config?"+apiV1SignedQuery().Encode(), nil, "")
	controller.Prepare()
	runClientPermissionAction(t, controller.GlobalConfig)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	payload := readAPIV1JSON(t, recorder)
	data, ok := payload["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("config payload = %#v", payload)
	}
	values, ok := data["values"].(map[string]interface{})
	if !ok {
		t.Fatalf("config values = %#v", data["values"])
	}
	for key, want := range map[string]string{
		"allow_user_register":      "true",
		"register_max_clients":     "3",
		"register_max_tunnels":     "0",
		"register_max_hosts":       "",
		"register_bandwidth_limit": "384",
	} {
		if got, _ := values[key].(string); got != want {
			t.Fatalf("config[%s] = %q, want %q", key, got, want)
		}
	}
}

func TestAPIV1GlobalConfigSaveOnlyChangesSubmittedBusinessSetting(t *testing.T) {
	withAPIV1TestAuthKey(t)
	path := withSettingsFixture(t, "web_port=8081\nflow_limit_type=total\nflow_store_interval=1\n"+
		"api_hmac_enabled=true\napi_hmac_time_window=60\napi_legacy_md5_enabled=true\n")
	settingsBefore := settings.Snapshot()
	t.Cleanup(func() {
		values := make(map[string]string, len(settingsBefore))
		for key, value := range settingsBefore {
			values[key] = value
		}
		if err := settings.Save(values, nil); err != nil {
			t.Fatalf("restore settings fixture: %v", err)
		}
	})

	form := url.Values{"flow_limit_type": {"actual"}}
	controller, recorder := newAPIV1TestController(
		t,
		http.MethodPost,
		"/api/v1/global/config?"+apiV1SignedQuery().Encode(),
		strings.NewReader(form.Encode()),
		"application/x-www-form-urlencoded",
	)
	controller.Prepare()
	runClientPermissionAction(t, controller.GlobalConfigSave)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	payload := readAPIV1JSON(t, recorder)
	if payload["status"] != float64(1) {
		t.Fatalf("save envelope = %#v", payload)
	}
	if got, _ := settings.Get("flow_limit_type"); got != "actual" {
		t.Fatalf("flow_limit_type = %q, want actual", got)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := settings.Get("flow_store_interval"); got != "1" {
		t.Fatalf("unsubmitted flow_store_interval was cleared: %q", got)
	}
	if !strings.Contains(string(content), "web_port=8081") {
		t.Fatalf("boot setting was removed: %s", content)
	}
}

func TestAPIV1GlobalConfigSaveEnablesSubmittedOptionalSetting(t *testing.T) {
	withAPIV1TestAuthKey(t)
	withSettingsFixture(t, "web_port=8081\napi_hmac_enabled=true\napi_hmac_time_window=60\napi_legacy_md5_enabled=true\n")
	settingsBefore := settings.Snapshot()
	t.Cleanup(func() {
		values := make(map[string]string, len(settingsBefore))
		for key, value := range settingsBefore {
			values[key] = value
		}
		if err := settings.Save(values, nil); err != nil {
			t.Fatalf("restore settings fixture: %v", err)
		}
	})

	form := url.Values{"allow_ports": {"60000-61000,62000-62100"}}
	controller, recorder := newAPIV1TestController(
		t,
		http.MethodPost,
		"/api/v1/global/config?"+apiV1SignedQuery().Encode(),
		strings.NewReader(form.Encode()),
		"application/x-www-form-urlencoded",
	)
	controller.Prepare()
	runClientPermissionAction(t, controller.GlobalConfigSave)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	payload := readAPIV1JSON(t, recorder)
	if payload["status"] != float64(1) {
		t.Fatalf("save envelope = %#v", payload)
	}
	if got, _ := settings.Get("allow_ports"); got != "60000-61000,62000-62100" {
		t.Fatalf("allow_ports = %q, want the submitted value", got)
	}
}

func TestAPIV1GlobalConfigSaveJSONUpdatesOptionalSetting(t *testing.T) {
	withAPIV1TestAuthKey(t)
	path := withSettingsFixture(t, "web_port=8081\nallow_ports=1000-2000\n"+
		"api_hmac_enabled=true\napi_hmac_time_window=60\napi_legacy_md5_enabled=true\n")
	settingsBefore := settings.Snapshot()
	t.Cleanup(func() {
		values := make(map[string]string, len(settingsBefore))
		for key, value := range settingsBefore {
			values[key] = value
		}
		if err := settings.Save(values, nil); err != nil {
			t.Fatalf("restore settings fixture: %v", err)
		}
	})

	body := `{"allow_ports":"31000-32000,33000"}`
	controller, recorder := newAPIV1TestController(
		t,
		http.MethodPost,
		"/api/v1/global/config?"+apiV1SignedQuery().Encode(),
		strings.NewReader(body),
		"application/json",
	)
	controller.Prepare()
	runClientPermissionAction(t, controller.GlobalConfigSave)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	payload := readAPIV1JSON(t, recorder)
	if payload["status"] != float64(1) {
		t.Fatalf("save envelope = %#v", payload)
	}
	if got, _ := settings.Get("allow_ports"); got != "31000-32000,33000" {
		t.Fatalf("allow_ports = %q, want the JSON-submitted value", got)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "web_port=8081") {
		t.Fatalf("unsubmitted boot setting was removed: %s", content)
	}
}
