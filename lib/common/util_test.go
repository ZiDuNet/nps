package common

import (
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astaxie/beego"
)

func TestResolveConfigPath(t *testing.T) {
	originalRunPath := ConfPath
	ConfPath = filepath.Join(t.TempDir(), "nps-root")
	t.Cleanup(func() { ConfPath = originalRunPath })

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "", want: ""},
		{name: "docker conf remapped", input: "/conf/webssl/fullchain.pem", want: filepath.Join(ConfPath, "conf/webssl/fullchain.pem")},
		{name: "absolute outside docker conf", input: "/etc/certs/fullchain.pem", want: "/etc/certs/fullchain.pem"},
		{name: "relative", input: "conf/webssl/fullchain.pem", want: filepath.Join(ConfPath, "conf/webssl/fullchain.pem")},
		{name: "inline PEM", input: "-----BEGIN CERTIFICATE-----\nABC\n-----END CERTIFICATE-----", want: "-----BEGIN CERTIFICATE-----\nABC\n-----END CERTIFICATE-----"},
		{name: "trimmed", input: "  conf/a.pem  ", want: filepath.Join(ConfPath, "conf/a.pem")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveConfigPath(tc.input); got != tc.want {
				t.Fatalf("ResolveConfigPath(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestInDoesNotMutateAddressList(t *testing.T) {
	addresses := []string{"10.0.0.3", "10.0.0.1", "10.0.0.2"}
	original := append([]string(nil), addresses...)
	if !in("10.0.0.1", addresses) {
		t.Fatal("expected address to be found")
	}
	for index := range addresses {
		if addresses[index] != original[index] {
			t.Fatalf("address list was reordered: got %#v, want %#v", addresses, original)
		}
	}
}

func TestIPAccessListsMatchExactAddresses(t *testing.T) {
	if !IsBlackIp("203.0.113.7:443", "test", []string{"203.0.113.7"}) {
		t.Fatal("exact IPv4 blacklist entry must match")
	}
	if !IsBlackIp("[2001:db8::7]:443", "test", []string{"2001:db8::7"}) {
		t.Fatal("exact IPv6 blacklist entry must match")
	}
	if IsBlackIp("203.0.113.7:443", "test", []string{"203.0.113.0/24"}) {
		t.Fatal("CIDR blacklist entry must not be treated as an exact IP match")
	}
}

func TestGetIPByAddrPreservesBareIPv6(t *testing.T) {
	for _, test := range []struct {
		address string
		want    string
	}{
		{address: "2001:db8::7", want: "2001:db8::7"},
		{address: "[2001:db8::7]:443", want: "2001:db8::7"},
		{address: "203.0.113.7:443", want: "203.0.113.7"},
	} {
		if got := GetIpByAddr(test.address); got != test.want {
			t.Fatalf("GetIpByAddr(%q) = %q, want %q", test.address, got, test.want)
		}
	}
}

func TestGetPortByAddrSupportsIPv6(t *testing.T) {
	for _, test := range []struct {
		address string
		want    int
	}{
		{address: "127.0.0.1:8024", want: 8024},
		{address: "[2001:db8::7]:443", want: 443},
		{address: "2001:db8::7", want: 0},
	} {
		if got := GetPortByAddr(test.address); got != test.want {
			t.Fatalf("GetPortByAddr(%q) = %d, want %d", test.address, got, test.want)
		}
	}
}

func TestIsPortRejectsOutOfRangeValues(t *testing.T) {
	if !IsPort("65535") {
		t.Fatal("65535 should be a valid port")
	}
	if IsPort("65536") {
		t.Fatal("65536 must be rejected")
	}
}

func TestGetServerIpByClientIpPrefersReachablePublicListener(t *testing.T) {
	clientIP := net.ParseIP("198.51.100.20")
	localIP := net.ParseIP("203.0.113.10")
	if got := GetServerIpByClientIp(clientIP, localIP); got != localIP.String() {
		t.Fatalf("server address = %q, want %q", got, localIP.String())
	}
}

func TestChangeHostAndHeaderForwardedModes(t *testing.T) {
	restore := setTestAppConfig(t, map[string]string{
		"http_add_origin_header":     "true",
		"trusted_proxy_ips":          "127.0.0.0/8",
		"http_forwarded_header_mode": "overwrite",
	})
	defer restore()

	request := newForwardedHeaderRequest()
	request.Header.Set("X-Forwarded-For", "198.51.100.7")
	request.Header.Set("X-Real-IP", "198.51.100.7")
	request.Header.Set("X-Forwarded-Host", "spoofed.example.com")
	request.Header.Set("X-Forwarded-Proto", "http")
	request.Header.Set("X-Forwarded-Port", "80")
	request.Header.Set("X-Real-Proto", "http")

	ChangeHostAndHeader(request, "", "", "203.0.113.9:12345")
	assertForwardedHeaders(t, request, map[string]string{
		"X-Forwarded-For":   "203.0.113.9",
		"X-Real-IP":         "203.0.113.9",
		"X-Forwarded-Host":  "portal.example.com",
		"X-Forwarded-Proto": "https",
		"X-Forwarded-Port":  "18443",
		"X-Real-Proto":      "https",
	})
}

func TestChangeHostAndHeaderPreserveAndTrustedModes(t *testing.T) {
	restore := setTestAppConfig(t, map[string]string{
		"http_add_origin_header":     "true",
		"trusted_proxy_ips":          "127.0.0.0/8",
		"http_forwarded_header_mode": "preserve",
	})
	defer restore()

	request := newForwardedHeaderRequest()
	setExistingForwardedHeaders(request)
	ChangeHostAndHeader(request, "", "", "203.0.113.9:12345")
	if got := request.Header.Get("X-Forwarded-For"); got != "198.51.100.7, 203.0.113.9" {
		t.Fatalf("preserve X-Forwarded-For = %q", got)
	}
	for _, key := range []string{"X-Real-IP", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port", "X-Real-Proto"} {
		if got := request.Header.Get(key); got != existingForwardedHeaderValue(key) {
			t.Fatalf("preserve %s = %q, want %q", key, got, existingForwardedHeaderValue(key))
		}
	}

	if err := beego.AppConfig.Set("http_forwarded_header_mode", "trusted"); err != nil {
		t.Fatal(err)
	}

	request = newForwardedHeaderRequest()
	setExistingForwardedHeaders(request)
	ChangeHostAndHeader(request, "", "", "203.0.113.9:12345")
	if got := request.Header.Get("X-Forwarded-For"); got != "203.0.113.9" {
		t.Fatalf("untrusted X-Forwarded-For = %q", got)
	}
	if got := request.Header.Get("X-Real-IP"); got != "203.0.113.9" {
		t.Fatalf("untrusted X-Real-IP = %q", got)
	}

	request = newForwardedHeaderRequest()
	setExistingForwardedHeaders(request)
	ChangeHostAndHeader(request, "", "", "127.0.0.1:12345")
	if got := request.Header.Get("X-Forwarded-For"); got != "198.51.100.7, 127.0.0.1" {
		t.Fatalf("trusted X-Forwarded-For = %q", got)
	}
	for _, key := range []string{"X-Real-IP", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port", "X-Real-Proto"} {
		if got := request.Header.Get(key); got != existingForwardedHeaderValue(key) {
			t.Fatalf("trusted %s = %q, want %q", key, got, existingForwardedHeaderValue(key))
		}
	}
}

func TestChangeHostAndHeaderDoesNotAddForwardedHeadersWhenDisabled(t *testing.T) {
	restore := setTestAppConfig(t, map[string]string{
		"http_add_origin_header": "false",
	})
	defer restore()

	request := newForwardedHeaderRequest()
	ChangeHostAndHeader(request, "backend.internal:8080", "X-Origin: nps", "203.0.113.9:12345")
	if request.Host != "backend.internal:8080" || request.Header.Get("X-Origin") != "nps" {
		t.Fatalf("host/header rewrite failed: host=%q headers=%v", request.Host, request.Header)
	}
	for _, key := range []string{"X-Forwarded-For", "X-Real-IP", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port", "X-Real-Proto"} {
		if value := request.Header.Get(key); value != "" {
			t.Fatalf("%s = %q, want empty", key, value)
		}
	}
}

func newForwardedHeaderRequest() *http.Request {
	request, err := http.NewRequest(http.MethodGet, "https://portal.example.com:18443/path", nil)
	if err != nil {
		panic(err)
	}
	request.Host = "portal.example.com:18443"
	return request
}

func setExistingForwardedHeaders(request *http.Request) {
	for key, value := range map[string]string{
		"X-Forwarded-For":   "198.51.100.7",
		"X-Real-IP":         "198.51.100.7",
		"X-Forwarded-Host":  "edge.example.com",
		"X-Forwarded-Proto": "https",
		"X-Forwarded-Port":  "443",
		"X-Real-Proto":      "https",
	} {
		request.Header.Set(key, value)
	}
}

func existingForwardedHeaderValue(key string) string {
	switch strings.ToLower(key) {
	case "x-real-ip":
		return "198.51.100.7"
	case "x-forwarded-host":
		return "edge.example.com"
	case "x-forwarded-proto", "x-real-proto":
		return "https"
	case "x-forwarded-port":
		return "443"
	default:
		return ""
	}
}

func assertForwardedHeaders(t *testing.T, request *http.Request, want map[string]string) {
	t.Helper()
	for key, expected := range want {
		if got := request.Header.Get(key); got != expected {
			t.Fatalf("%s = %q, want %q", key, got, expected)
		}
	}
}

func setTestAppConfig(t *testing.T, values map[string]string) func() {
	t.Helper()
	previous := make(map[string]string, len(values))
	for key, value := range values {
		previous[key] = beego.AppConfig.String(key)
		if err := beego.AppConfig.Set(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	return func() {
		for key, value := range previous {
			_ = beego.AppConfig.Set(key, value)
		}
	}
}
