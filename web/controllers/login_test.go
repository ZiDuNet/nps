package controllers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/ipreg"
	"github.com/astaxie/beego"
	beecontext "github.com/astaxie/beego/context"
	"github.com/astaxie/beego/session"
)

func TestStandaloneIPRegisterHandlerUsesPeerIPAndOnlyPublicRoutes(t *testing.T) {
	db := useClientPermissionTestDb(t)
	db.JsonDb.Clients.Store(23, &file.Client{
		Id:               23,
		VerifyKey:        "client-register-key",
		RegisterMaxHours: 1,
		Status:           true,
	})
	oldMaxHours := beego.AppConfig.String("ip_register_max_hours")
	oldPublicRegisterKey := beego.AppConfig.String("public_register_key")
	oldPublicVKey := beego.AppConfig.String("public_vkey")
	_ = beego.AppConfig.Set("ip_register_max_hours", "2")
	_ = beego.AppConfig.Set("public_register_key", "")
	_ = beego.AppConfig.Set("public_vkey", "")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("ip_register_max_hours", oldMaxHours)
		_ = beego.AppConfig.Set("public_register_key", oldPublicRegisterKey)
		_ = beego.AppConfig.Set("public_vkey", oldPublicVKey)
		ipreg.Reset()
	})
	ipreg.Reset()

	handler := IPRegisterStandaloneHandler()
	get := httptest.NewRequest(http.MethodGet, "http://register.test/", nil)
	get.RemoteAddr = "198.51.100.23:43210"
	get.Header.Set("X-Forwarded-For", "203.0.113.90")
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK || !strings.Contains(getResponse.Body.String(), "198.51.100.23") {
		t.Fatalf("registration page response = %d %q", getResponse.Code, getResponse.Body.String())
	}
	if strings.Contains(getResponse.Body.String(), "203.0.113.90") {
		t.Fatal("standalone registration page trusted X-Forwarded-For")
	}

	form := url.Values{"key": {"client-register-key"}, "hours": {"99"}}
	post := httptest.NewRequest(http.MethodPost, "http://register.test/ip/register", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.RemoteAddr = "198.51.100.23:43210"
	post.Header.Set("X-Forwarded-For", "203.0.113.90")
	postResponse := httptest.NewRecorder()
	handler.ServeHTTP(postResponse, post)
	var result map[string]interface{}
	if err := json.Unmarshal(postResponse.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode registration response: %v; body=%q", err, postResponse.Body.String())
	}
	if postResponse.Code != http.StatusOK || result["status"] != float64(1) || result["ip"] != "198.51.100.23" {
		t.Fatalf("registration response = %d %#v", postResponse.Code, result)
	}
	if !ipreg.RegisteredString(23, "198.51.100.23:8080") || ipreg.RegisteredString(23, "203.0.113.90:8080") {
		t.Fatal("registration did not bind to the listener peer address")
	}

	privateRoute := httptest.NewRecorder()
	handler.ServeHTTP(privateRoute, httptest.NewRequest(http.MethodGet, "http://register.test/client/list", nil))
	if privateRoute.Code != http.StatusNotFound {
		t.Fatalf("unrelated route returned %d, want 404", privateRoute.Code)
	}
}

func TestRequestVisitorIPDoesNotTrustForwardedHeaders(t *testing.T) {
	oldAllow := beego.AppConfig.String("allow_x_real_ip")
	oldTrusted := beego.AppConfig.String("trusted_proxy_ips")
	oldHTTPOnly := beego.AppConfig.String("x_nps_http_only")
	_ = beego.AppConfig.Set("allow_x_real_ip", "false")
	_ = beego.AppConfig.Set("trusted_proxy_ips", "127.0.0.1")
	_ = beego.AppConfig.Set("x_nps_http_only", "")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("allow_x_real_ip", oldAllow)
		_ = beego.AppConfig.Set("trusted_proxy_ips", oldTrusted)
		_ = beego.AppConfig.Set("x_nps_http_only", oldHTTPOnly)
	})
	request := httptest.NewRequest(http.MethodGet, "http://console.test/ip/register", nil)
	request.RemoteAddr = "198.51.100.23:43210"
	request.Header.Set("X-Forwarded-For", "203.0.113.90")
	recorder := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(recorder, request)

	if got := requestVisitorIP(ctx); got != "198.51.100.23" {
		t.Fatalf("requestVisitorIP() = %q, want TCP peer IP; forwarded header must be ignored", got)
	}
}

func TestRequestVisitorIPUsesTrustedProxyRealIP(t *testing.T) {
	oldAllow := beego.AppConfig.String("allow_x_real_ip")
	oldTrusted := beego.AppConfig.String("trusted_proxy_ips")
	oldHTTPOnly := beego.AppConfig.String("x_nps_http_only")
	_ = beego.AppConfig.Set("allow_x_real_ip", "true")
	_ = beego.AppConfig.Set("trusted_proxy_ips", "192.0.2.10")
	_ = beego.AppConfig.Set("x_nps_http_only", "")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("allow_x_real_ip", oldAllow)
		_ = beego.AppConfig.Set("trusted_proxy_ips", oldTrusted)
		_ = beego.AppConfig.Set("x_nps_http_only", oldHTTPOnly)
	})

	request := httptest.NewRequest(http.MethodGet, "http://console.test/ip/register", nil)
	request.RemoteAddr = "192.0.2.10:43210"
	request.Header.Set("X-Real-IP", "203.0.113.90")
	request.Header.Set("X-Forwarded-For", "198.51.100.4, 192.0.2.10")
	ctx := beecontext.NewContext()
	ctx.Reset(httptest.NewRecorder(), request)
	if got := requestVisitorIP(ctx); got != "203.0.113.90" {
		t.Fatalf("requestVisitorIP() = %q, want trusted X-Real-IP", got)
	}
	if got, source := requestVisitorIPDetails(request); got != "203.0.113.90" || source != "可信反代的 X-Real-IP" {
		t.Fatalf("requestVisitorIPDetails() = %q, %q", got, source)
	}

	request.Header.Set("X-Real-IP", "not-an-ip")
	if got := requestVisitorIPFromRequest(request); got != "198.51.100.4" {
		t.Fatalf("requestVisitorIP() fallback = %q, want first X-Forwarded-For address", got)
	}
}

func TestRequestVisitorIPUsesHTTPOnlySecret(t *testing.T) {
	oldAllow := beego.AppConfig.String("allow_x_real_ip")
	oldTrusted := beego.AppConfig.String("trusted_proxy_ips")
	oldHTTPOnly := beego.AppConfig.String("x_nps_http_only")
	_ = beego.AppConfig.Set("allow_x_real_ip", "false")
	_ = beego.AppConfig.Set("trusted_proxy_ips", "192.0.2.10")
	_ = beego.AppConfig.Set("x_nps_http_only", "only-for-proxy")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("allow_x_real_ip", oldAllow)
		_ = beego.AppConfig.Set("trusted_proxy_ips", oldTrusted)
		_ = beego.AppConfig.Set("x_nps_http_only", oldHTTPOnly)
	})

	request := httptest.NewRequest(http.MethodGet, "http://console.test/ip/register", nil)
	request.RemoteAddr = "198.51.100.23:43210"
	request.Header.Set("X-NPS-Http-Only", "only-for-proxy")
	request.Header.Set("X-Real-IP", "203.0.113.90")
	if got := requestVisitorIPFromRequest(request); got != "203.0.113.90" {
		t.Fatalf("requestVisitorIP() with HTTP-only secret = %q, want header address", got)
	}

	request.Header.Set("X-NPS-Http-Only", "wrong")
	if got := requestVisitorIPFromRequest(request); got != "198.51.100.23" {
		t.Fatalf("requestVisitorIP() with wrong secret = %q, want peer address", got)
	}
}

func TestVerifyLoginPoWConsumesShortLivedChallenge(t *testing.T) {
	oldBits := beego.AppConfig.String("pow_bits")
	oldForce := beego.AppConfig.String("force_pow")
	_ = beego.AppConfig.Set("pow_bits", "1")
	_ = beego.AppConfig.Set("force_pow", "true")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("pow_bits", oldBits)
		_ = beego.AppConfig.Set("force_pow", oldForce)
	})
	nonce := "pow-test"
	now := time.Now()
	if !powChallenges.store(nonce, now.Add(time.Minute), now) {
		t.Fatal("could not store test challenge")
	}
	solution := ""
	for i := 0; i < 100; i++ {
		candidate := string(rune('0' + i))
		digest := sha256.Sum256([]byte(nonce + "\x00" + candidate))
		if digest[0]&0x80 == 0 {
			solution = candidate
			break
		}
	}
	if solution == "" {
		t.Fatal("could not find a one-bit PoW test solution")
	}
	if !verifyLoginPoW("alice", nonce, solution) {
		t.Fatal("valid PoW solution was rejected")
	}
	if verifyLoginPoW("alice", nonce, solution) {
		t.Fatal("PoW challenge was reusable")
	}
}

func TestPoWChallengeStoreIsBoundedAndReclaimsExpiredEntries(t *testing.T) {
	store := newPoWChallengeStore(2)
	base := time.Unix(1_700_000_000, 0)
	for _, nonce := range []string{"one", "two"} {
		if !store.store(nonce, base.Add(time.Minute), base) {
			t.Fatalf("could not store challenge %q", nonce)
		}
	}
	if store.store("three", base.Add(time.Minute), base) {
		t.Fatal("challenge store exceeded its configured capacity")
	}
	if got := store.len(); got != 2 {
		t.Fatalf("challenge count = %d, want capacity 2", got)
	}

	if !store.store("three", base.Add(2*time.Minute), base.Add(time.Minute)) {
		t.Fatal("expired challenges should be reclaimed before rejecting a new one")
	}
	if got := store.len(); got != 1 {
		t.Fatalf("challenge count after expiry cleanup = %d, want 1", got)
	}
	if !store.consume("three", base.Add(time.Minute+time.Second)) {
		t.Fatal("fresh challenge was not consumable")
	}
	if store.consume("three", base.Add(time.Minute+time.Second)) {
		t.Fatal("challenge was reusable")
	}
}

func TestPoWChallengeStoreConcurrentCapacity(t *testing.T) {
	const capacity = 64
	store := newPoWChallengeStore(capacity)
	now := time.Now()
	var accepted atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for index := 0; index < capacity*2; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			if store.store(fmt.Sprintf("challenge-%d", index), now.Add(time.Minute), now) {
				accepted.Add(1)
			}
		}(index)
	}
	close(start)
	wg.Wait()
	if got := int(accepted.Load()); got != capacity {
		t.Fatalf("accepted challenges = %d, want %d", got, capacity)
	}
	if got := store.len(); got != capacity {
		t.Fatalf("stored challenges = %d, want %d", got, capacity)
	}
}

func TestReserveLoginAttemptDoesNotCountImplicitLoginPageChecks(t *testing.T) {
	limiter := newLoginAttemptLimiter(8)
	if !allowLoginAttemptWith(limiter, "implicit-login-page", "nobody", false, time.Now()) {
		t.Fatal("implicit login page checks should remain allowed")
	}
	if ipCount, userCount := limiter.lengths(); ipCount != 0 || userCount != 0 {
		t.Fatal("implicit login page checks must not create a failure record")
	}
}

func TestReserveLoginAttemptLimitsConcurrentFailures(t *testing.T) {
	const ip = "concurrent-login-attempts"
	limiter := newLoginAttemptLimiter(32)

	var allowed int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < maxLoginFailures*4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if limiter.reserve(ip, "alice", time.Now()) {
				atomic.AddInt32(&allowed, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := int(atomic.LoadInt32(&allowed)); got != maxLoginFailures {
		t.Fatalf("allowed concurrent login attempts = %d, want %d", got, maxLoginFailures)
	}
}

func TestReserveLoginAttemptExpiresFailureWindow(t *testing.T) {
	const ip = "expired-login-attempts"
	limiter := newLoginAttemptLimiter(8)

	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < maxLoginFailures; i++ {
		if !limiter.reserve(ip, "alice", now) {
			t.Fatalf("attempt %d unexpectedly rejected", i+1)
		}
	}
	if limiter.reserve(ip, "alice", now) {
		t.Fatal("attempt after the failure limit should be rejected")
	}
	if !limiter.reserve(ip, "alice", now.Add(loginFailureWindow)) {
		t.Fatal("attempt after the failure window should be admitted")
	}
}

func TestLoginAttemptLimiterAppliesIPAndUsernameDimensions(t *testing.T) {
	limiter := newLoginAttemptLimiter(32)
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < maxLoginFailures; i++ {
		if !limiter.reserve("192.0.2.1", "Alice", base.Add(time.Duration(i)*time.Millisecond)) {
			t.Fatalf("attempt %d was unexpectedly rejected", i+1)
		}
	}
	if limiter.reserve("192.0.2.2", "ALICE", base.Add(time.Second)) {
		t.Fatal("username limit should apply across source IPs and case variants")
	}
	if !limiter.reserve("192.0.2.2", "Bob", base.Add(time.Second)) {
		t.Fatal("a different username and IP should have an independent allowance")
	}
	limiter.clear("192.0.2.1", "Alice")
	if !limiter.reserve("192.0.2.2", "alice", base.Add(2*time.Second)) {
		t.Fatal("successful authentication should clear the IP and username reservations")
	}
}

func TestLoginAttemptLimiterBoundsStateAndReclaimsExpiredEntries(t *testing.T) {
	limiter := newLoginAttemptLimiter(2)
	base := time.Unix(1_700_000_000, 0)
	if !limiter.reserve("192.0.2.1", "one", base) || !limiter.reserve("192.0.2.2", "two", base) {
		t.Fatal("initial reservations were rejected")
	}
	if limiter.reserve("192.0.2.3", "three", base) {
		t.Fatal("limiter exceeded its configured capacity")
	}
	if ipCount, userCount := limiter.lengths(); ipCount != 2 || userCount != 2 {
		t.Fatalf("store lengths = (%d,%d), want (2,2)", ipCount, userCount)
	}
	if !limiter.reserve("192.0.2.3", "three", base.Add(loginFailureWindow)) {
		t.Fatal("expired entries should be reclaimed before a new attempt")
	}
	if ipCount, userCount := limiter.lengths(); ipCount > 2 || userCount > 2 {
		t.Fatalf("store exceeded capacity after reclamation: (%d,%d)", ipCount, userCount)
	}
}

func TestLoginAttemptIP(t *testing.T) {
	if got := loginAttemptIP("127.0.0.1:8080"); got != "127.0.0.1" {
		t.Fatalf("IPv4 login IP = %q, want 127.0.0.1", got)
	}
	if got := loginAttemptIP("[2001:db8::1]:8080"); got != "2001:db8::1" {
		t.Fatalf("IPv6 login IP = %q, want 2001:db8::1", got)
	}
	if got := loginAttemptIP("malformed"); got != "malformed" {
		t.Fatalf("malformed login address = %q, want fallback", got)
	}
}

func TestRegenerateSessionWithoutManagerIsSafe(t *testing.T) {
	var controller LoginController
	controller.regenerateSession()
}

func TestRegenerateSessionWithCookieProviderKeepsAuthenticationStore(t *testing.T) {
	oldManager := beego.GlobalSessions
	t.Cleanup(func() { beego.GlobalSessions = oldManager })

	providerConfig, err := json.Marshal(map[string]string{
		"securityKey":  "login-test-signing-key",
		"blockKey":     "0123456789abcdef",
		"securityName": "login-test-session",
		"cookieName":   "nps_session",
	})
	if err != nil {
		t.Fatalf("encode cookie provider config: %v", err)
	}
	manager, err := session.NewManager("cookie", &session.ManagerConfig{
		CookieName:      "nps_session",
		EnableSetCookie: true,
		Gclifetime:      3600,
		Maxlifetime:     3600,
		CookieLifeTime:  3600,
		ProviderConfig:  string(providerConfig),
	})
	if err != nil {
		t.Fatalf("create cookie session manager: %v", err)
	}
	beego.GlobalSessions = manager

	store, err := manager.GetProvider().SessionRead("pre-login-cookie")
	if err != nil {
		t.Fatalf("read cookie session: %v", err)
	}
	if err := store.Set("preAuthValue", "must be cleared"); err != nil {
		t.Fatalf("seed cookie session: %v", err)
	}

	controller := &LoginController{}
	controller.CruSession = store
	controller.regenerateSession()
	if controller.CruSession == nil {
		t.Fatal("cookie provider session was lost during regeneration")
	}
	if got := controller.GetSession("preAuthValue"); got != nil {
		t.Fatalf("pre-login session data survived regeneration: %#v", got)
	}

	clearAuthenticationSession(controller.DelSession)
	controller.SetSession("auth", true)
	request := httptest.NewRequest("POST", "http://console.test/login/verify", nil)
	recorder := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(recorder, request)
	controller.Ctx = ctx
	controller.releaseCookieSession()
	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("cookie provider did not write an authentication cookie")
	}
	encoded := cookies[len(cookies)-1].Value
	decoded, err := url.QueryUnescape(encoded)
	if err != nil {
		t.Fatalf("decode authentication cookie: %v", err)
	}
	restored, err := manager.GetProvider().SessionRead(decoded)
	if err != nil {
		t.Fatalf("read regenerated authentication cookie: %v", err)
	}
	if got := restored.Get("auth"); got != true {
		t.Fatalf("authentication state in regenerated cookie = %#v, want true", got)
	}
}

func TestLoginIndexReleasesCookieSessionBeforeRendering(t *testing.T) {
	providerConfig, err := json.Marshal(map[string]string{
		"securityKey":  "login-index-signing-key",
		"blockKey":     "0123456789abcdef",
		"securityName": "login-index-session",
		"cookieName":   "nps_session",
	})
	if err != nil {
		t.Fatalf("encode cookie provider config: %v", err)
	}
	manager, err := session.NewManager("cookie", &session.ManagerConfig{
		CookieName:      "nps_session",
		EnableSetCookie: true,
		Gclifetime:      3600,
		Maxlifetime:     3600,
		ProviderConfig:  string(providerConfig),
	})
	if err != nil {
		t.Fatalf("create cookie session manager: %v", err)
	}
	store, err := manager.GetProvider().SessionRead("pre-login-cookie")
	if err != nil {
		t.Fatalf("read cookie session: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://console.test/login/index", nil)
	recorder := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(recorder, request)
	controller := &LoginController{Controller: beego.Controller{
		Ctx:        ctx,
		Data:       make(map[interface{}]interface{}),
		CruSession: store,
	}}

	oldUser := beego.AppConfig.String("web_username")
	oldPassword := beego.AppConfig.String("web_password")
	oldBaseURL := beego.AppConfig.String("web_base_url")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("web_username", oldUser)
		_ = beego.AppConfig.Set("web_password", oldPassword)
		_ = beego.AppConfig.Set("web_base_url", oldBaseURL)
	})
	_ = beego.AppConfig.Set("web_username", "admin")
	_ = beego.AppConfig.Set("web_password", "secret")
	_ = beego.AppConfig.Set("web_base_url", "")

	controller.Index()
	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login page did not release its cookie session")
	}
	if len(cookies[len(cookies)-1].Value) <= 32 {
		t.Fatalf("login page released an unencrypted session id: %q", cookies[len(cookies)-1].Value)
	}
	if got := store.Get("login_nonce"); got == nil || got == "" {
		t.Fatalf("login nonce was not stored in cookie session: %#v", got)
	}
}

func TestCaptchaFilterPathFollowsWebBaseURL(t *testing.T) {
	tests := []struct {
		name string
		base string
		want string
	}{
		{name: "root", base: "", want: "/captcha/"},
		{name: "subpath", base: "/nps", want: "/nps/captcha/"},
		{name: "trailing slash", base: "/nps/", want: "/nps/captcha/"},
		{name: "missing leading slash", base: "nps", want: "/nps/captcha/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := captchaFilterPath(tt.base); got != tt.want {
				t.Fatalf("captchaFilterPath(%q) = %q, want %q", tt.base, got, tt.want)
			}
		})
	}
}

func TestSetNoStoreDisablesProxyCaching(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://console.test/login/index", nil)
	recorder := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(recorder, request)

	setNoStore(ctx)
	header := recorder.Header()
	if got := header.Get("Cache-Control"); got != "no-store, no-cache, must-revalidate, max-age=0" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := header.Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma = %q", got)
	}
	if got := header.Get("Expires"); got != "0" {
		t.Fatalf("Expires = %q", got)
	}
}
