package file

import (
	"encoding/base64"
	"net/http/httptest"
	"testing"
)

func TestParseBasicAuth(t *testing.T) {
	accounts, err := ParseBasicAuth("alice:secret\r\nbob:p:a:ss")
	if err != nil {
		t.Fatalf("ParseBasicAuth returned error: %v", err)
	}
	if len(accounts) != 2 || accounts["alice"] != "secret" || accounts["bob"] != "p:a:ss" {
		t.Fatalf("parsed accounts = %#v", accounts)
	}
}

func TestParseBasicAuthRejectsInvalidAccounts(t *testing.T) {
	for _, value := range []string{
		"missing-separator",
		":password",
		"username:",
		"alice:one\nalice:two",
		"alice:one\ninvalid",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := ParseBasicAuth(value); err == nil {
				t.Fatalf("ParseBasicAuth(%q) unexpectedly succeeded", value)
			}
		})
	}
}

func TestCheckBasicAuth(t *testing.T) {
	encode := func(value string) string { return "Basic " + base64.StdEncoding.EncodeToString([]byte(value)) }
	for _, tc := range []struct {
		name   string
		header string
		proxy  bool
		want   bool
	}{
		{name: "missing", want: false},
		{name: "wrong password", header: encode("alice:wrong"), want: false},
		{name: "valid authorization", header: encode("alice:secret"), want: true},
		{name: "valid proxy authorization", header: encode("bob:p:a:ss"), proxy: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://example.test/", nil)
			if tc.proxy {
				req.Header.Set("Proxy-Authorization", tc.header)
			} else if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			if got := CheckBasicAuth(req, "alice:secret\nbob:p:a:ss"); got != tc.want {
				t.Fatalf("CheckBasicAuth() = %t, want %t", got, tc.want)
			}
		})
	}
}
