package controllers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego"
	beecontext "github.com/astaxie/beego/context"
)

func TestBaseControllerDoesNotAcceptVersionedAPIHMAC(t *testing.T) {
	previousKey := beego.AppConfig.String("auth_key")
	previousHMAC := beego.AppConfig.DefaultBool("api_hmac_enabled", true)
	_ = beego.AppConfig.Set("auth_key", "base-controller-hmac-test-key")
	_ = beego.AppConfig.Set("api_hmac_enabled", "true")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("auth_key", previousKey)
		_ = beego.AppConfig.Set("api_hmac_enabled", strconv.FormatBool(previousHMAC))
	})

	body := []byte(`{"sensitive":"body"}`)
	request := httptest.NewRequest(http.MethodPost, "http://console.test/client/add", bytes.NewReader(body))
	bodySum := sha256.Sum256(body)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce, err := newNPSHMACNonce()
	if err != nil {
		t.Fatal(err)
	}
	keyID := npsHMACAuthKeyID
	signature, err := signNPSHMAC("base-controller-hmac-test-key", request, hex.EncodeToString(bodySum[:]), timestamp, nonce, keyID)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", npsHMACAuthorizationScheme+" "+signature)
	request.Header.Set("X-NPS-Timestamp", timestamp)
	request.Header.Set("X-NPS-Nonce", nonce)
	request.Header.Set("X-NPS-Key-Id", keyID)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(response, request)
	session := &clientPermissionSession{values: map[interface{}]interface{}{}}
	ctx.Input.CruSession = session
	controller := &BaseController{}
	controller.Init(ctx, "BaseController", "Add", controller)
	controller.CruSession = session

	runClientPermissionAction(t, controller.Prepare)
	if controller.apiAuthorized {
		t.Fatal("legacy MVC controller accepted an NPS-HMAC credential")
	}
	if response.Code != http.StatusFound {
		t.Fatalf("legacy MVC response status = %d, want login redirect %d", response.Code, http.StatusFound)
	}
}

func TestBaseControllerAcceptsLegacyMD5OnConsoleRoutesOnlyWhenEnabled(t *testing.T) {
	previousKey := beego.AppConfig.String("auth_key")
	previousLegacy := beego.AppConfig.DefaultBool("api_legacy_md5_enabled", false)
	_ = beego.AppConfig.Set("auth_key", "base-controller-md5-test-key")
	_ = beego.AppConfig.Set("api_legacy_md5_enabled", "true")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("auth_key", previousKey)
		_ = beego.AppConfig.Set("api_legacy_md5_enabled", strconv.FormatBool(previousLegacy))
	})

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	form := url.Values{
		"auth_key":  {crypt.Md5("base-controller-md5-test-key" + timestamp)},
		"timestamp": {timestamp},
	}
	request := httptest.NewRequest(http.MethodPost, "http://console.test/client/list/", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(response, request)
	session := &clientPermissionSession{values: map[interface{}]interface{}{"isAdmin": true}}
	ctx.Input.CruSession = session
	controller := &BaseController{}
	controller.Init(ctx, "ClientController", "List", controller)
	controller.CruSession = session

	controller.Prepare()
	if !controller.apiAuthorized {
		t.Fatal("legacy MVC controller did not accept explicitly enabled MD5 compatibility")
	}
	if response.Code == http.StatusFound {
		t.Fatalf("legacy MVC response redirected to login despite valid migration credential")
	}
}

func TestBaseControllerRejectsLegacyMD5WhenDisabled(t *testing.T) {
	previousKey := beego.AppConfig.String("auth_key")
	previousLegacy := beego.AppConfig.DefaultBool("api_legacy_md5_enabled", false)
	_ = beego.AppConfig.Set("auth_key", "base-controller-md5-test-key")
	_ = beego.AppConfig.Set("api_legacy_md5_enabled", "false")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("auth_key", previousKey)
		_ = beego.AppConfig.Set("api_legacy_md5_enabled", strconv.FormatBool(previousLegacy))
	})

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	form := url.Values{
		"auth_key":  {crypt.Md5("base-controller-md5-test-key" + timestamp)},
		"timestamp": {timestamp},
	}
	request := httptest.NewRequest(http.MethodPost, "http://console.test/client/list/", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(response, request)
	session := &clientPermissionSession{values: map[interface{}]interface{}{}}
	ctx.Input.CruSession = session
	controller := &BaseController{}
	controller.Init(ctx, "ClientController", "List", controller)
	controller.CruSession = session

	runClientPermissionAction(t, controller.Prepare)
	if controller.apiAuthorized {
		t.Fatal("legacy MVC controller accepted MD5 credentials while compatibility is disabled")
	}
	if response.Code != http.StatusFound {
		t.Fatalf("legacy MVC response status = %d, want login redirect %d", response.Code, http.StatusFound)
	}
}

func TestLegacyMD5RouteAllowlistRequiresExactManagementRoutes(t *testing.T) {
	previousBase := beego.AppConfig.String("web_base_url")
	t.Cleanup(func() { _ = beego.AppConfig.Set("web_base_url", previousBase) })

	tests := []struct {
		name   string
		method string
		target string
		base   string
		want   bool
	}{
		{name: "documented legacy list route", method: http.MethodPost, target: "/client/list/", want: true},
		{name: "versioned API list route", method: http.MethodGet, target: "/api/v1/clients", want: true},
		{name: "versioned API route under base path", method: http.MethodGet, target: "/panel/api/v1/clients/3", base: "/panel", want: true},
		{name: "versioned item read route", method: http.MethodGet, target: "/api/v1/tunnels/12", want: true},
		{name: "versioned item update route", method: http.MethodPatch, target: "/api/v1/hosts/12", want: true},
		{name: "versioned user token status route", method: http.MethodGet, target: "/api/v1/users/12/token/status", want: true},
		{name: "versioned user token revoke route", method: http.MethodPost, target: "/api/v1/users/12/token/revoke", want: true},
		{name: "versioned client instance disconnect", method: http.MethodPost, target: "/api/v1/clients/12/instances/disconnect", want: true},
		{name: "management route under base path", method: http.MethodPost, target: "/panel/client/list", base: "/panel", want: true},
		{name: "public versioned discovery route", method: http.MethodGet, target: "/api/v1/", want: false},
		{name: "public versioned time route", method: http.MethodGet, target: "/api/v1/time", want: false},
		{name: "public versioned IP registration route", method: http.MethodPost, target: "/api/v1/ip-register", want: false},
		{name: "unknown versioned API route", method: http.MethodGet, target: "/api/v1/future", want: false},
		{name: "unknown item resource", method: http.MethodGet, target: "/api/v1/widgets/12", want: false},
		{name: "unsupported resource action", method: http.MethodPost, target: "/api/v1/clients/12/start", want: false},
		{name: "wrong item method", method: http.MethodPost, target: "/api/v1/clients/12", want: false},
		{name: "list route rejects wrong method", method: http.MethodDelete, target: "/api/v1/clients", want: false},
		{name: "non-decimal identifier", method: http.MethodGet, target: "/api/v1/clients/not-an-id", want: false},
		{name: "zero identifier", method: http.MethodGet, target: "/api/v1/clients/0", want: false},
		{name: "unregistered nested action", method: http.MethodPost, target: "/api/v1/clients/12/instances/unknown", want: false},
		{name: "user token status rejects wrong method", method: http.MethodPost, target: "/api/v1/users/12/token/status", want: false},
		{name: "user token revoke rejects wrong method", method: http.MethodGet, target: "/api/v1/users/12/token/revoke", want: false},
		{name: "wrong method", method: http.MethodGet, target: "/client/list", want: false},
		{name: "login route", method: http.MethodPost, target: "/login/index", want: false},
		{name: "public auth route", method: http.MethodPost, target: "/auth/gettime", want: false},
		{name: "base path prefix collision", method: http.MethodPost, target: "/panelx/client/list", base: "/panel", want: false},
		{name: "encoded allowlisted character", method: http.MethodPost, target: "/%63lient/list", want: false},
		{name: "encoded slash", method: http.MethodPost, target: "/client%2flist", want: false},
		{name: "duplicate slash", method: http.MethodPost, target: "/client//list", want: false},
		{name: "dot segment", method: http.MethodPost, target: "/client/../login/index", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = beego.AppConfig.Set("web_base_url", tt.base)
			request := httptest.NewRequest(tt.method, "http://console.test"+tt.target, nil)
			if got := legacyMD5APIRequestAllowed(request); got != tt.want {
				t.Fatalf("legacyMD5APIRequestAllowed(%s %s) = %v, want %v", tt.method, tt.target, got, tt.want)
			}
		})
	}
}

func TestLegacyMD5CredentialsRejectAmbiguousOrMalformedInput(t *testing.T) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	signature := crypt.Md5("base-controller-md5-test-key" + timestamp)
	tests := []struct {
		name   string
		target string
		body   string
		want   bool
	}{
		{name: "query credentials", target: "/client/list?auth_key=" + signature + "&timestamp=" + timestamp, want: true},
		{name: "form credentials", target: "/client/list", body: url.Values{"auth_key": {signature}, "timestamp": {timestamp}}.Encode(), want: true},
		{name: "duplicate query and form credential", target: "/client/list?auth_key=" + signature + "&timestamp=" + timestamp, body: url.Values{"auth_key": {signature}, "timestamp": {timestamp}}.Encode(), want: false},
		{name: "malformed query", target: "/client/list?auth_key=%ZZ&timestamp=" + timestamp, want: false},
		{name: "duplicate query credential", target: "/client/list?auth_key=" + signature + "&auth_key=" + signature + "&timestamp=" + timestamp, want: false},
		{name: "noncanonical timestamp", target: "/client/list?auth_key=" + signature + "&timestamp=0" + timestamp, want: false},
		{name: "overflow timestamp", target: "/client/list?auth_key=" + signature + "&timestamp=999999999999999999999999999999999", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body *strings.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			} else {
				body = strings.NewReader("")
			}
			request := httptest.NewRequest(http.MethodPost, "http://console.test"+tt.target, body)
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			_, _, _, got := legacyMD5QueryCredentials(request)
			if got != tt.want {
				t.Fatalf("legacyMD5QueryCredentials() accepted = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAjaxDetailKeepsLegacyAndStandardFields(t *testing.T) {
	s := &BaseController{requestID: "test-request"}
	ok := s.ajaxDetail(map[string]interface{}{"Id": 1}, nil)
	if ok["code"] != 1 || ok["status"] != 1 || ok["msg"] != "ok" {
		t.Fatalf("detail success envelope = %#v", ok)
	}
	if ok["data"] == nil {
		t.Fatal("detail success envelope must include data")
	}
	if _, present := ok["request_id"]; !present {
		t.Fatal("detail success envelope must include request_id")
	}

	fail := s.ajaxDetail(nil, errors.New("boom"))
	if fail["code"] != 0 || fail["status"] != 0 || fail["msg"] != "boom" {
		t.Fatalf("detail error envelope = %#v", fail)
	}
	if _, present := fail["data"]; present {
		t.Fatal("detail error envelope must not include data")
	}
}

func TestPublicAccessPortFallsBackToListenerAndHonorsReverseProxyPort(t *testing.T) {
	oldAccess := beego.AppConfig.String("http_proxy_access_port")
	oldListener := beego.AppConfig.String("http_proxy_port")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("http_proxy_access_port", oldAccess)
		_ = beego.AppConfig.Set("http_proxy_port", oldListener)
	})
	_ = beego.AppConfig.Set("http_proxy_port", "80")
	_ = beego.AppConfig.Set("http_proxy_access_port", "")
	if got := publicAccessPort("http_proxy_access_port", "http_proxy_port"); got != "80" {
		t.Fatalf("fallback public port = %q, want 80", got)
	}
	_ = beego.AppConfig.Set("http_proxy_access_port", "30111")
	if got := publicAccessPort("http_proxy_access_port", "http_proxy_port"); got != "30111" {
		t.Fatalf("configured public port = %q, want 30111", got)
	}
}

func TestPaginationMetadataPreservesLegacyListFields(t *testing.T) {
	if got := pageNumber(0, 20); got != 1 {
		t.Fatalf("pageNumber(0, 20) = %d, want 1", got)
	}
	if got := pageNumber(40, 20); got != 3 {
		t.Fatalf("pageNumber(40, 20) = %d, want 3", got)
	}
	if got := pageNumber(0, 0); got != 1 {
		t.Fatalf("pageNumber(0, 0) = %d, want 1", got)
	}
	if !hasMoreRows(0, 20, 21) || hasMoreRows(20, 20, 21) {
		t.Fatal("hasMoreRows did not respect offset, page size and total")
	}
}

func TestWithRequestMetaAddsNonBreakingAPIFields(t *testing.T) {
	controller := &BaseController{requestID: "req-test-1"}
	response := controller.withRequestMeta(ajax("ok", 1))
	if response["status"] != 1 || response["msg"] != "ok" {
		t.Fatalf("legacy response fields changed: %#v", response)
	}
	if response["code"] != "ok" || response["request_id"] != "req-test-1" {
		t.Fatalf("response metadata missing: %#v", response)
	}
	failed := controller.withRequestMeta(ajax("bad", 0))
	if failed["code"] != "error" {
		t.Fatalf("error response code = %#v, want error", failed["code"])
	}
	list := controller.withRequestMeta(map[string]interface{}{"rows": []string{}, "total": 0})
	if list["code"] != "ok" {
		t.Fatalf("list response code = %#v, want ok", list["code"])
	}
}

func TestSessionBoolAcceptsCommonSessionValues(t *testing.T) {
	tests := []struct {
		name  string
		value interface{}
		want  bool
	}{
		{name: "bool true", value: true, want: true},
		{name: "bool false", value: false, want: false},
		{name: "string true", value: "true", want: true},
		{name: "string one", value: "1", want: true},
		{name: "string zero", value: "0", want: false},
		{name: "numeric one", value: 1, want: true},
		{name: "numeric zero", value: 0, want: false},
		{name: "nil", value: nil, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sessionBool(test.value); got != test.want {
				t.Fatalf("sessionBool(%#v) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestPublicServerHostSeparatesHostnameFromSchemeAndPort(t *testing.T) {
	tests := []struct {
		address string
		want    string
	}{
		{address: "nps.example.com", want: "nps.example.com"},
		{address: "nps.example.com:8024", want: "nps.example.com"},
		{address: "https://nps.example.com:30111/nps", want: "nps.example.com"},
		{address: "[2001:db8::1]:8024", want: "2001:db8::1"},
		{address: "", want: ""},
	}
	for _, test := range tests {
		if got := publicServerHost(test.address); got != test.want {
			t.Errorf("publicServerHost(%q) = %q, want %q", test.address, got, test.want)
		}
	}
}

func TestPublicBridgeHostPrefersConfiguredBridgeAddress(t *testing.T) {
	if got := publicBridgeHost("panel.example:8081", "https://web.example:443/nps", "bridge.example:9443"); got != "bridge.example" {
		t.Fatalf("public bridge host = %q, want bridge.example", got)
	}
	if got := publicBridgeHost("panel.example:8081", "https://web.example:443/nps", ""); got != "web.example" {
		t.Fatalf("bridge host fallback = %q, want web.example", got)
	}
	if got := publicBridgeHost("panel.example:8081", "", ""); got != "panel.example" {
		t.Fatalf("request-host fallback = %q, want panel.example", got)
	}
}

func TestTunnelListRowsExposeConfiguredPublicAccessAddress(t *testing.T) {
	db := file.GetDb()
	oldGlobal := db.GetGlobal()
	t.Cleanup(func() { _ = db.SaveGlobal(oldGlobal) })
	if err := db.SaveGlobal(&file.Glob{ServerUrl: "https://nps.example.com:8081/nps"}); err != nil {
		t.Fatalf("save global: %v", err)
	}
	tunnel := &file.Tunnel{Id: 7, Port: 10022, Mode: "tcp"}
	rows := newTunnelListRows([]*file.Tunnel{tunnel}, "fallback.example.com:9999")
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].AccessHost != "nps.example.com" {
		t.Fatalf("access host = %q, want nps.example.com", rows[0].AccessHost)
	}
	if rows[0].AccessAddr != "nps.example.com:10022" || rows[0].AccessAddress != rows[0].AccessAddr {
		t.Fatalf("access address = %q/%q, want nps.example.com:10022", rows[0].AccessAddr, rows[0].AccessAddress)
	}
	if rows[0].AccessURL != "tcp://nps.example.com:10022" {
		t.Fatalf("access URL = %q, want tcp://nps.example.com:10022", rows[0].AccessURL)
	}
}

func TestTunnelListRowsFallbackToRequestHostAndFormatIPv6(t *testing.T) {
	db := file.GetDb()
	oldGlobal := db.GetGlobal()
	t.Cleanup(func() { _ = db.SaveGlobal(oldGlobal) })
	if err := db.SaveGlobal(&file.Glob{}); err != nil {
		t.Fatalf("save global: %v", err)
	}
	rows := newTunnelListRows([]*file.Tunnel{{Port: 443}}, "[2001:db8::10]:8081")
	if len(rows) != 1 || rows[0].AccessAddr != "[2001:db8::10]:443" {
		t.Fatalf("IPv6 access row = %#v", rows)
	}
}

func TestTunnelListRowsAlwaysExposeResourceRateFields(t *testing.T) {
	tunnel := &file.Tunnel{
		Id:         9,
		Port:       19001,
		Mode:       "tcp",
		RateLimits: file.RateLimits{UploadLimit: 128, DownloadLimit: 1024},
	}
	rows := newTunnelListRows([]*file.Tunnel{tunnel}, "nps.example.com")
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	encoded, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatalf("marshal tunnel row: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode tunnel row: %v", err)
	}
	for _, key := range []string{"UploadLimit", "DownloadLimit", "UploadRate", "DownloadRate"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("tunnel API row is missing %s: %s", key, encoded)
		}
	}
	if payload["UploadLimit"] != float64(128) || payload["DownloadLimit"] != float64(1024) {
		t.Fatalf("unexpected resource limits in API row: %s", encoded)
	}

	zeroRows := newTunnelListRows([]*file.Tunnel{{Id: 10, Port: 19002, Mode: "tcp"}}, "nps.example.com")
	zeroEncoded, err := json.Marshal(zeroRows[0])
	if err != nil {
		t.Fatalf("marshal unlimited tunnel row: %v", err)
	}
	var zeroPayload map[string]interface{}
	if err := json.Unmarshal(zeroEncoded, &zeroPayload); err != nil {
		t.Fatalf("decode unlimited tunnel row: %v", err)
	}
	for _, key := range []string{"UploadLimit", "DownloadLimit", "UploadRate", "DownloadRate"} {
		if _, ok := zeroPayload[key]; !ok {
			t.Fatalf("unlimited tunnel API row is missing %s: %s", key, zeroEncoded)
		}
	}
}

func TestTunnelListRowsNeverExposeMixedProxyCredentials(t *testing.T) {
	tunnel := &file.Tunnel{
		Id:        11,
		Port:      19003,
		Mode:      "mixProxy",
		ProxyAuth: "proxy-user:super-secret",
	}
	row := newTunnelListRows([]*file.Tunnel{tunnel}, "nps.example.com")[0]
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal tunnel row: %v", err)
	}
	if bytes.Contains(encoded, []byte(`"ProxyAuth":`)) ||
		bytes.Contains(encoded, []byte("proxy-user:super-secret")) {
		t.Fatalf("mixed proxy credentials leaked through tunnel API row: %s", encoded)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode tunnel row: %v", err)
	}
	if payload["ProxyAuthConfigured"] != true {
		t.Fatalf("ProxyAuthConfigured = %#v, want true", payload["ProxyAuthConfigured"])
	}
}

func TestIsAdminAuthorizedKeepsAPICredentialsRequestScoped(t *testing.T) {
	if !isAdminAuthorized(true, false) {
		t.Fatal("signed API authorization should be effective for its request")
	}
	if isAdminAuthorized(false, false) {
		t.Fatal("an absent API credential and non-admin session must not elevate access")
	}
	if !isAdminAuthorized(false, true) {
		t.Fatal("an admin session should remain effective")
	}
}

func TestAllowedClientIDsForUserUseCurrentMembership(t *testing.T) {
	stale := map[int]struct{}{1: {}}
	fresh := map[int]struct{}{2: {}, 3: {}}
	loadedFor := 0
	got := allowedClientIDsForPrincipal(sessionPrincipalUser, 7, 0, stale, func(userID int) map[int]struct{} {
		loadedFor = userID
		return fresh
	})
	if loadedFor != 7 {
		t.Fatalf("membership lookup used user %d, want 7", loadedFor)
	}
	if _, ok := got[1]; ok {
		t.Fatalf("stale session client membership was retained: %#v", got)
	}
	if _, ok := got[2]; !ok {
		t.Fatalf("fresh database membership was not used: %#v", got)
	}
}

func TestActiveNonAdminPrincipalRequiresExpectedIdentity(t *testing.T) {
	tests := []struct {
		name                     string
		principal                string
		userID, clientID         int
		userActive, clientActive bool
		want                     bool
	}{
		{name: "active user", principal: sessionPrincipalUser, userID: 7, userActive: true, want: true},
		{name: "disabled user", principal: sessionPrincipalUser, userID: 7, userActive: false, want: false},
		{name: "active client", principal: sessionPrincipalClient, clientID: 4, clientActive: true, want: true},
		{name: "disabled client", principal: sessionPrincipalClient, clientID: 4, clientActive: false, want: false},
		{name: "legacy session is invalid", principal: "", userID: 7, userActive: true, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := activeNonAdminPrincipal(test.principal, test.userID, test.clientID, test.userActive, test.clientActive); got != test.want {
				t.Fatalf("activeNonAdminPrincipal() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestClearAuthenticationSessionRemovesEveryIdentityValue(t *testing.T) {
	removed := make(map[string]bool)
	clearAuthenticationSession(func(key interface{}) {
		name, ok := key.(string)
		if !ok {
			t.Fatalf("unexpected session key type %T", key)
		}
		removed[name] = true
	})
	for _, key := range []string{"auth", "isAdmin", "clientId", "clientIds", "userId", "username", sessionPrincipalKey} {
		if !removed[key] {
			t.Fatalf("session key %q was not cleared", key)
		}
	}
}

func TestPublicLayoutHidesAdministratorNavigationForOrdinaryUsers(t *testing.T) {
	tpl := template.Must(template.ParseFiles("../views/public/layout.html"))
	data := map[string]interface{}{
		"isAdmin":           false,
		"web_base_url":      "",
		"version":           "test",
		"github_repository": "ZiDuNet/nps",
		"LayoutContent":     "",
	}
	var rendered bytes.Buffer
	if err := tpl.Execute(&rendered, data); err != nil {
		t.Fatalf("execute public layout: %v", err)
	}
	if strings.Contains(rendered.String(), "/global/index") {
		t.Fatal("ordinary user navigation must not contain the global settings link")
	}
	if !strings.Contains(rendered.String(), "https://github.com/ZiDuNet/nps/releases/latest") {
		t.Fatal("console layout must link the version indicator to the canonical release page")
	}

	data["isAdmin"] = true
	rendered.Reset()
	if err := tpl.Execute(&rendered, data); err != nil {
		t.Fatalf("execute administrator layout: %v", err)
	}
	if !strings.Contains(rendered.String(), "/global/index") {
		t.Fatal("administrator navigation must retain the global settings link")
	}
}
