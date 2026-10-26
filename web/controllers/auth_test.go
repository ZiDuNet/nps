package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego"
	beecontext "github.com/astaxie/beego/context"
)

func TestIPWhiteAuthPostFormIgnoresQueryCredentials(t *testing.T) {
	request, err := http.NewRequest(
		http.MethodPost,
		"/auth/ipwhiteauth?vkey=query-vkey&ip=198.51.100.10&pass=query-secret",
		strings.NewReader("vkey=body-vkey&ip=203.0.113.7&pass=body-secret"),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	vkey, ip, password, err := ipWhiteAuthPostForm(request)
	if err != nil {
		t.Fatalf("parse POST form: %v", err)
	}
	if vkey != "body-vkey" || ip != "203.0.113.7" || password != "body-secret" {
		t.Fatalf("credentials = (%q, %q, %q), want only POST body values", vkey, ip, password)
	}
}

func TestIPWhiteAuthPostFormRejectsNonPOSTRequests(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "/auth/ipwhiteauth?pass=secret", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, _, _, err := ipWhiteAuthPostForm(request); err != errIPWhiteAuthMethod {
		t.Fatalf("GET request error = %v, want %v", err, errIPWhiteAuthMethod)
	}
}

func newAuthTestController(method, url string) (*AuthController, *httptest.ResponseRecorder) {
	request := httptest.NewRequest(method, url, nil)
	recorder := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(recorder, request)
	controller := &AuthController{}
	controller.Init(ctx, "AuthController", "Test", controller)
	return controller, recorder
}

func readAuthJSON(t *testing.T, recorder *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var payload map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	return payload
}

func TestV1TimeUsesStandardEnvelope(t *testing.T) {
	controller, recorder := newAuthTestController(http.MethodGet, "http://console.test/v1/auth/time")
	controller.V1Time()
	payload := readAuthJSON(t, recorder)
	if payload["status"] != float64(1) || payload["msg"] != "ok" {
		t.Fatalf("v1 time response = %#v, want status/msg envelope", payload)
	}
	data, ok := payload["data"].(map[string]interface{})
	if !ok || data["time"] == nil {
		t.Fatalf("v1 time response data = %#v, want time field", payload["data"])
	}
}

func TestV1AuthKeyUsesStandardEnvelope(t *testing.T) {
	oldCryptKey := beego.AppConfig.String("auth_crypt_key")
	oldAuthKey := beego.AppConfig.String("auth_key")
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("auth_crypt_key", oldCryptKey)
		_ = beego.AppConfig.Set("auth_key", oldAuthKey)
	})

	_ = beego.AppConfig.Set("auth_crypt_key", "short")
	_ = beego.AppConfig.Set("auth_key", "test-auth-key")
	controller, recorder := newAuthTestController(http.MethodGet, "http://console.test/v1/auth/authkey")
	controller.V1AuthKey()
	if payload := readAuthJSON(t, recorder); payload["status"] != float64(0) || payload["msg"] == "" {
		t.Fatalf("short auth_crypt_key response = %#v, want status 0 with message", payload)
	}

	_ = beego.AppConfig.Set("auth_crypt_key", "0123456789abcdef")
	controller, recorder = newAuthTestController(http.MethodGet, "http://console.test/v1/auth/authkey")
	controller.V1AuthKey()
	payload := readAuthJSON(t, recorder)
	if payload["status"] != float64(1) {
		t.Fatalf("v1 authkey response = %#v, want status 1", payload)
	}
	data, ok := payload["data"].(map[string]interface{})
	if !ok || data["crypt_auth_key"] == "" || data["crypt_type"] != "aes cbc" {
		t.Fatalf("v1 authkey data = %#v, want crypt_auth_key and crypt_type", payload["data"])
	}
}

func TestV1IPWhiteAuthUsesStandardEnvelope(t *testing.T) {
	controller, recorder := newAuthTestController(http.MethodGet, "http://console.test/v1/auth/ipwhiteauth")
	controller.V1IPWhiteAuth()
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
	if payload := readAuthJSON(t, recorder); payload["status"] != float64(0) || payload["msg"] == "" {
		t.Fatalf("method response = %#v, want status 0 with message", payload)
	}

	controller, recorder = newAuthTestController(http.MethodPost, "http://console.test/v1/auth/ipwhiteauth")
	controller.V1IPWhiteAuth()
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("empty POST status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if payload := readAuthJSON(t, recorder); payload["status"] != float64(0) || payload["msg"] == "" {
		t.Fatalf("empty POST response = %#v, want status 0 with message", payload)
	}
}

func TestNormalizeIPWhiteIP(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: " 203.0.113.7 ", want: "203.0.113.7"},
		{input: "2001:db8::7", want: "2001:db8::7"},
		{input: "not-an-ip", want: ""},
		{input: "203.0.113.7\\nspoof", want: ""},
	} {
		if got := normalizeIPWhiteIP(test.input); got != test.want {
			t.Fatalf("normalizeIPWhiteIP(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestClientIPWhitePasswordMatchesLegacyEscapedValue(t *testing.T) {
	client := &file.Client{IpWhitePass: "a&amp;b"}
	if !clientIPWhitePasswordMatches(client, "a&b") {
		t.Fatal("legacy HTML-escaped whitelist password should compare using its original value")
	}
	if clientIPWhitePasswordMatches(client, "a&amp;b") {
		t.Fatal("encoded spelling must not authenticate as the original password")
	}
}

func TestAddClientIPWhiteListSerializesConcurrentUpdates(t *testing.T) {
	client := &file.Client{}
	addresses := []string{"203.0.113.1", "203.0.113.2", "2001:db8::1"}
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		address := addresses[i%len(addresses)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			addClientIPWhiteList(client, address)
		}()
	}
	wg.Wait()

	client.RLock()
	defer client.RUnlock()
	if len(client.IpWhiteList) != len(addresses) {
		t.Fatalf("whitelist entries = %v, want one entry for each of %v", client.IpWhiteList, addresses)
	}
}
