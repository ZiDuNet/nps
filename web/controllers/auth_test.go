package controllers

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"ehang.io/nps/lib/file"
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
