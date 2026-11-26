package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestCreatePlatformHostRejectsCustomDomain(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	})
	cases := []string{
		`{"client_id":1,"host":"app.example.com","target":"127.0.0.1:80"}`,
		`{"client_id":1,"domain_mode":"custom","target":"127.0.0.1:80"}`,
	}
	for _, payload := range cases {
		err := c.createPlatformHost(payload, "", "")
		if err == nil || !strings.Contains(err.Error(), "仅支持平台域名") {
			t.Fatalf("payload %s returned %v, want a custom-domain rejection", payload, err)
		}
	}
}

func TestCreatePlatformHostUsesPlatformDomainAndRandomPrefix(t *testing.T) {
	var posted map[string]any
	c, out := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/platform-domains" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"status":1,"code":"ok","msg":"ok","data":[{"id":"pd1","wildcard":"*.example.com","certificate_configured":true}]}`))
		case r.URL.Path == "/api/v1/hosts" && r.Method == http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Errorf("decode host payload: %v", err)
			}
			_, _ = w.Write([]byte(`{"status":1,"code":"ok","msg":"add success","data":{"id":7}}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})
	if err := c.createPlatformHost(`{"client_id":3,"target":"127.0.0.1:8080"}`, "", ""); err != nil {
		t.Fatalf("create host: %v", err)
	}
	if posted["domain_mode"] != "platform" {
		t.Fatalf("domain_mode = %#v, want platform", posted["domain_mode"])
	}
	if posted["platform_domain_id"] != "pd1" {
		t.Fatalf("platform_domain_id = %#v, want pd1", posted["platform_domain_id"])
	}
	prefix, _ := posted["platform_prefix"].(string)
	if len(prefix) != 8 {
		t.Fatalf("platform_prefix = %q, want 8 characters", prefix)
	}
	if _, exists := posted["host"]; exists {
		t.Fatalf("payload still contains host: %#v", posted)
	}
	got := out.String()
	if !strings.Contains(got, "#7") || !strings.Contains(got, prefix+".example.com") {
		t.Fatalf("output = %q, want the created host and id", got)
	}
}

func TestCreatePlatformHostRetriesRandomPrefixOnConflict(t *testing.T) {
	attempts := 0
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/platform-domains":
			_, _ = w.Write([]byte(`{"status":1,"data":[{"id":"pd1","wildcard":"*.example.com","certificate_configured":true}]}`))
		case r.URL.Path == "/api/v1/hosts":
			attempts++
			if attempts == 1 {
				_, _ = w.Write([]byte(`{"status":0,"msg":"add fail, 平台域名与路径规则已被使用"}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":1,"msg":"add success","data":{"id":8}}`))
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
	})
	if err := c.createPlatformHost(`{"client_id":3,"target":"127.0.0.1:8080"}`, "", ""); err != nil {
		t.Fatalf("create host: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want a retry after the conflict", attempts)
	}
}

func TestPickPlatformDomainPrefersCertificateForHTTPS(t *testing.T) {
	domains := []platformDomain{
		{ID: "http-only", Wildcard: "*.plain.example.com"},
		{ID: "with-cert", Wildcard: "*.example.com", CertificateConfigured: true},
	}
	domain, err := pickPlatformDomain(domains, "", "all")
	if err != nil {
		t.Fatal(err)
	}
	if domain.ID != "with-cert" {
		t.Fatalf("picked %q, want with-cert", domain.ID)
	}
	domain, err = pickPlatformDomain(domains, "*.PLAIN.example.com", "http")
	if err != nil {
		t.Fatal(err)
	}
	if domain.ID != "http-only" {
		t.Fatalf("picked %q, want http-only", domain.ID)
	}
	if _, err := pickPlatformDomain([]platformDomain{{ID: "x", Wildcard: "*.x.example.com"}}, "", "https"); err == nil {
		t.Fatal("expected HTTPS without a certificate to be rejected")
	}
}
