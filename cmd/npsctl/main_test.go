package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"ehang.io/nps/lib/version"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*client, *bytes.Buffer) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	out := &bytes.Buffer{}
	return &client{
		server: server.URL,
		token:  "npsu_test",
		http:   server.Client(),
		out:    out,
	}, out
}

func TestListKeepsRowsAndTotalAndUsesOffset(t *testing.T) {
	c, out := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("offset"); got != "40" {
			t.Errorf("offset = %q, want 40", got)
		}
		if got := r.URL.Query().Get("limit"); got != "10" {
			t.Errorf("limit = %q, want 10", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":1,"code":"ok","msg":"ok","rows":[{"id":1,"remark":"a"}],"total":1,"request_id":"rid"}`))
	})
	c.jsonOutput = true
	if err := c.list("/api/v1/clients", 10, 40); err != nil {
		t.Fatalf("list returned error: %v", err)
	}
	got := out.String()
	for _, want := range []string{`"rows"`, `"total"`, `"id": 1`, `"request_id": "rid"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, `"data": null`) {
		t.Fatalf("output still contains fake empty data: %s", got)
	}
}

func TestListDefaultOutputIsReadableTable(t *testing.T) {
	c, out := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":1,"rows":[{"Id":7,"Remark":"edge","Status":true,"IsConnect":false}],"total":3}`))
	})
	if err := c.list("/api/v1/clients", 0, 0); err != nil {
		t.Fatalf("list returned error: %v", err)
	}
	got := out.String()
	for _, want := range []string{"Id", "Remark", "Status", "edge", "true", "显示 1 条（总计 3 条）"} {
		if !strings.Contains(got, want) {
			t.Fatalf("table output missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, `"rows"`) {
		t.Fatalf("default list output should be human-readable, got JSON: %s", got)
	}
}

func TestFieldsWritesSelectedTSVAndRejectsUnknownField(t *testing.T) {
	c, out := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":1,"rows":[{"Id":7,"Remark":"edge"}],"total":1}`))
	})
	c.fields = "Id,Remark"
	if err := c.list("/api/v1/clients", 0, 0); err != nil {
		t.Fatalf("list returned error: %v", err)
	}
	if got, want := out.String(), "Id\tRemark\n7\tedge\n"; got != want {
		t.Fatalf("TSV output = %q, want %q", got, want)
	}

	out.Reset()
	c.fields = "Id,Missing"
	err := c.list("/api/v1/clients", 0, 0)
	if err == nil || !strings.Contains(err.Error(), `字段 "Missing" 不存在`) {
		t.Fatalf("expected missing field error, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("failed TSV request should not write partial output: %q", out.String())
	}
}

func TestDefaultAuditTableMatchesSourceIPResponseFieldCase(t *testing.T) {
	c, out := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":1,"rows":[{"ID":9,"Time":"2026-11-22T10:00:00Z","SourceIP":"203.0.113.8","Action":"auth.login"}],"total":1}`))
	})
	if err := c.list("/api/v1/audit", 10, 0); err != nil {
		t.Fatalf("audit list: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "SourceIP") || !strings.Contains(got, "203.0.113.8") {
		t.Fatalf("audit table dropped SourceIP due to field casing: %s", got)
	}
}

func TestFieldsRejectsResponseWithoutRowsOrData(t *testing.T) {
	c, out := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":1,"msg":"ok"}`))
	})
	c.fields = "status"
	if err := c.list("/api/v1/clients", 0, 0); err == nil || !strings.Contains(err.Error(), "没有 rows 或 data") {
		t.Fatalf("expected missing list data error, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("missing list data must not emit the response envelope as a row: %q", out.String())
	}
}

func TestHTTPErrorStatusFails(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":0,"code":"error","error_code":"boom","msg":"bad"}`))
	})
	err := c.get("/api/v1/clients")
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
	if !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAdminRequestUsesHMACHeadersAndDoesNotLeakSignatureIntoURL(t *testing.T) {
	secret := "test-admin-secret"
	body := `{"remark":"hello"}`
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.URL.Query().Has("auth_key") || r.URL.Query().Has("timestamp") {
			t.Errorf("signature was leaked into URL: %s", r.URL.String())
		}
		if got := r.Header.Get("X-NPS-Key-Id"); got != "auth_key" {
			t.Errorf("key id = %q, want auth_key", got)
		}
		if got := r.Header.Get("X-NPS-Timestamp"); got == "" {
			t.Error("timestamp header missing")
		}
		nonce := r.Header.Get("X-NPS-Nonce")
		if nonce == "" {
			t.Error("nonce header missing")
		}
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "NPS-HMAC ")
		if provided == r.Header.Get("Authorization") {
			t.Fatalf("unexpected authorization header %q", r.Header.Get("Authorization"))
		}
		requestBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		bodyHash := sha256.Sum256(requestBody)
		canonical := strings.Join([]string{
			r.Method,
			r.URL.EscapedPath(),
			r.URL.Query().Encode(),
			hex.EncodeToString(bodyHash[:]),
			r.Header.Get("X-NPS-Timestamp"),
			nonce,
			"auth_key",
		}, "\n")
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(canonical))
		want := hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(provided), []byte(want)) {
			t.Errorf("signature = %q, want %q; canonical=%q", provided, want, canonical)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":1,"data":{"ok":true}}`))
	}))
	defer server.Close()
	c := &client{server: server.URL, key: secret, http: server.Client(), out: &bytes.Buffer{}}
	if err := c.post("/api/v1/clients?api_token=credential&search=hello", body); err != nil {
		t.Fatalf("signed POST: %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1", requestCount)
	}
}

func TestAdminKeyIDCanOverridePrefixCollision(t *testing.T) {
	for _, test := range []struct {
		name, secret, requested, want string
	}{
		{name: "auto managed key", secret: "npsa_managed", requested: "auto", want: "managed_admin"},
		{name: "explicit managed key", secret: "npsa_managed", requested: "managed_admin", want: "managed_admin"},
		{name: "explicit auth key overrides prefix", secret: "npsa_conf-secret", requested: "auth_key", want: "auth_key"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("X-NPS-Key-Id"); got != test.want {
					t.Errorf("key id = %q, want %q", got, test.want)
				}
				requestBody, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request body: %v", err)
				}
				bodyHash := sha256.Sum256(requestBody)
				canonical := strings.Join([]string{
					r.Method,
					r.URL.EscapedPath(),
					r.URL.Query().Encode(),
					hex.EncodeToString(bodyHash[:]),
					r.Header.Get("X-NPS-Timestamp"),
					r.Header.Get("X-NPS-Nonce"),
					test.want,
				}, "\n")
				mac := hmac.New(sha256.New, []byte(test.secret))
				_, _ = mac.Write([]byte(canonical))
				provided := strings.TrimPrefix(r.Header.Get("Authorization"), "NPS-HMAC ")
				if !hmac.Equal([]byte(provided), []byte(hex.EncodeToString(mac.Sum(nil)))) {
					t.Errorf("signature does not match key id %q and canonical query %q", test.want, r.URL.RawQuery)
				}
				_, _ = w.Write([]byte(`{"status":1,"code":"ok"}`))
			}))
			defer server.Close()
			c := &client{server: server.URL, key: test.secret, keyID: test.requested, http: server.Client(), out: &bytes.Buffer{}}
			if err := c.get("/api/v1/overview?api_token=bound"); err != nil {
				t.Fatalf("request: %v", err)
			}
		})
	}
}

func TestAdminSigningRejectsMalformedCanonicalQueries(t *testing.T) {
	for _, rawQuery := range []string{
		"search=%ZZ",
		"search=one;limit=2",
	} {
		t.Run(rawQuery, func(t *testing.T) {
			req := &http.Request{
				Method: http.MethodGet,
				URL: &url.URL{
					Path:     "/api/v1/clients",
					RawQuery: rawQuery,
				},
			}
			if err := signAdminRequest(req, "test-secret", "auth_key", nil); err == nil {
				t.Fatalf("signAdminRequest accepted malformed query %q", rawQuery)
			}
		})
	}
}

func TestAdminSigningCanonicalizesEscapedPathDuplicateAndEmptyQueryValues(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet,
		"http://nps.test/api%2fv1/%E4%B8%AD?z=&a=1&a=2&plus=a+b", nil)
	if err := signAdminRequest(req, "test-secret", "auth_key", nil); err != nil {
		t.Fatalf("signAdminRequest: %v", err)
	}

	query, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		t.Fatalf("parse test query: %v", err)
	}
	canonicalQuery := query.Encode()
	if want := "a=1&a=2&plus=a+b&z="; canonicalQuery != want {
		t.Fatalf("canonical query = %q, want %q", canonicalQuery, want)
	}
	path := req.URL.EscapedPath()
	if want := "/api%2fv1/%E4%B8%AD"; path != want {
		t.Fatalf("escaped path = %q, want %q", path, want)
	}

	bodySum := sha256.Sum256(nil)
	canonical := strings.Join([]string{
		http.MethodGet,
		path,
		canonicalQuery,
		hex.EncodeToString(bodySum[:]),
		req.Header.Get("X-NPS-Timestamp"),
		req.Header.Get("X-NPS-Nonce"),
		"auth_key",
	}, "\n")
	mac := hmac.New(sha256.New, []byte("test-secret"))
	_, _ = mac.Write([]byte(canonical))
	wantSignature := hex.EncodeToString(mac.Sum(nil))
	gotSignature := strings.TrimPrefix(req.Header.Get("Authorization"), "NPS-HMAC ")
	if !hmac.Equal([]byte(gotSignature), []byte(wantSignature)) {
		t.Fatalf("signature mismatch for canonical request %q", canonical)
	}
}

func TestListRejectsMalformedQueryInsteadOfDroppingIt(t *testing.T) {
	c := &client{server: "http://127.0.0.1:8081", token: "npsu_test"}
	for _, path := range []string{
		"/api/v1/clients?search=%ZZ",
		"/api/v1/clients?search=one;limit=2",
	} {
		if err := c.list(path, 0, 0); err == nil || !strings.Contains(err.Error(), "查询串格式无效") {
			t.Fatalf("list(%q) should reject malformed query, got %v", path, err)
		}
	}
}

func TestKeyIDValidationAndHelp(t *testing.T) {
	t.Setenv("NPS_KEY", "")
	t.Setenv("NPS_TOKEN", "")
	if err := run([]string{"--key-id", "bogus", "version"}); err == nil || !strings.Contains(err.Error(), "--key-id") {
		t.Fatalf("invalid key-id should fail, got %v", err)
	}
	if err := run([]string{"--token", "npsu_test", "--key-id", "auth_key", "version"}); err == nil || !strings.Contains(err.Error(), "仅适用于 --key") {
		t.Fatalf("key-id with user token should fail, got %v", err)
	}
	var stdout, stderr bytes.Buffer
	if err := runWithWriters([]string{"--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("help: %v", err)
	}
	if !strings.Contains(stdout.String(), "--key-id TYPE") {
		t.Fatalf("help missing --key-id: %s", stdout.String())
	}
}

func TestNonJSONErrorStatusFails(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	})
	err := c.get("/api/v1/overview")
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") || !strings.Contains(err.Error(), "bad gateway") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientGetValidatesIDBeforeCredential(t *testing.T) {
	c := &client{}
	err := c.clientCmd([]string{"get"}, "")
	if err == nil || !strings.Contains(err.Error(), "用法") {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestClientDeleteRejectsNonNumericID(t *testing.T) {
	c := &client{}
	err := c.clientCmd([]string{"delete", "abc"}, "")
	if err == nil || !strings.Contains(err.Error(), "正整数") {
		t.Fatalf("expected numeric id error, got %v", err)
	}
}

func TestRequireDataRejectsInvalidJSON(t *testing.T) {
	if err := requireData("client create", "{bad"); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}

func TestRequireDataRejectsNonObjectJSON(t *testing.T) {
	for _, input := range []string{`[]`, `null`, `"text"`, `1`} {
		if err := requireData("client create", input); err == nil {
			t.Fatalf("expected non-object JSON %s to fail", input)
		}
	}
}

func TestMCPConfigIncludesHTTPType(t *testing.T) {
	c := &client{server: "https://nps.example.com", out: &bytes.Buffer{}}
	if err := c.printMCP(); err != nil {
		t.Fatalf("printMCP: %v", err)
	}
	out := c.out.(*bytes.Buffer).String()
	if !strings.Contains(out, `"type": "http"`) {
		t.Fatalf("missing type=http: %s", out)
	}
	if !strings.Contains(out, `"url": "https://nps.example.com/mcp"`) {
		t.Fatalf("unexpected url: %s", out)
	}
}

func TestSuccessHTTPStatusWithoutEnvelopeStatusFails(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"msg":"unexpected shape"}`))
	})
	err := c.get("/api/v1/overview")
	if err == nil || !strings.Contains(err.Error(), "unexpected shape") {
		t.Fatalf("expected status=0 response to fail, got %v", err)
	}
}

func TestAmbiguousCredentialsFail(t *testing.T) {
	c := &client{server: "https://nps.example.com", token: "npsu_x", key: "secret"}
	err := c.get("/api/v1/overview")
	if err == nil || !strings.Contains(err.Error(), "凭据冲突") {
		t.Fatalf("expected credential conflict, got %v", err)
	}
}

func TestServerURLValidation(t *testing.T) {
	for _, server := range []string{"nps.example.com", "ftp://nps.example.com", "https://", "https://user:pass@nps.example.com"} {
		c := &client{server: server, token: "npsu_x", out: &bytes.Buffer{}}
		if err := c.get("/api/v1/overview"); err == nil || !strings.Contains(err.Error(), "--server") {
			t.Fatalf("server %q should be rejected, got %v", server, err)
		}
	}
}

func TestHelpCanFollowSubcommand(t *testing.T) {
	t.Setenv("NPS_TOKEN", "")
	t.Setenv("NPS_KEY", "")
	if err := run([]string{"clients", "-h"}); err != nil {
		t.Fatalf("trailing help should succeed: %v", err)
	}
}

func TestUnknownTrailingArgumentsFail(t *testing.T) {
	if err := run([]string{"clients", "junk"}); err == nil || !strings.Contains(err.Error(), "额外参数") {
		t.Fatalf("expected trailing list argument error, got %v", err)
	}
	c := &client{}
	if err := c.clientCmd([]string{"get", "1", "extra"}, ""); err == nil || !strings.Contains(err.Error(), "参数数量") {
		t.Fatalf("expected exact argument error, got %v", err)
	}
}

func TestMCPConfigUsesManagedAdminKeyOnly(t *testing.T) {
	c := &client{server: "https://nps.example.com", key: "npsa_managed", out: &bytes.Buffer{}}
	if err := c.printMCP(); err != nil {
		t.Fatalf("printMCP: %v", err)
	}
	if out := c.out.(*bytes.Buffer).String(); !strings.Contains(out, "Bearer npsa_managed") {
		t.Fatalf("managed key should be used as MCP bearer: %s", out)
	}

	c = &client{server: "https://nps.example.com", key: "raw-auth-key", out: &bytes.Buffer{}}
	if err := c.printMCP(); err != nil {
		t.Fatalf("printMCP: %v", err)
	}
	out := c.out.(*bytes.Buffer).String()
	if strings.Contains(out, "raw-auth-key") || !strings.Contains(out, "Bearer YOUR_TOKEN") {
		t.Fatalf("raw auth_key must not be exposed as an MCP bearer: %s", out)
	}
	if strings.Contains(out, "${env:NPS_TOKEN}") || !strings.Contains(out, "不会展开") {
		t.Fatalf("MCP output should not claim unsupported environment interpolation: %s", out)
	}
}

func TestMCPConfigUsesNpsTokenEnvironmentValue(t *testing.T) {
	t.Setenv("NPS_TOKEN", "npsu_from_env")
	var out bytes.Buffer
	if err := runWithWriters([]string{"mcp"}, &out, io.Discard); err != nil {
		t.Fatalf("mcp: %v", err)
	}
	if !strings.Contains(out.String(), "Bearer npsu_from_env") {
		t.Fatalf("NPS_TOKEN should be written to the generated config: %s", out.String())
	}
}

func TestVersionEntryPoints(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}, {"-version"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := runWithWriters(args, &stdout, &stderr); err != nil {
				t.Fatalf("run %v: %v", args, err)
			}
			if got := strings.TrimSpace(stdout.String()); got != version.VERSION {
				t.Fatalf("version output = %q, want %q", got, version.VERSION)
			}
			if stderr.Len() != 0 {
				t.Fatalf("unexpected stderr: %s", stderr.String())
			}
		})
	}
}

func TestHTTPClientTimeoutAndCustomCA(t *testing.T) {
	if _, err := newHTTPClient(0, ""); err == nil || !strings.Contains(err.Error(), "--timeout") {
		t.Fatalf("expected invalid timeout error, got %v", err)
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":1,"data":{"ok":true}}`))
	}))
	defer server.Close()

	caFile := t.TempDir() + "/ca.pem"
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatalf("write test CA: %v", err)
	}
	httpClient, err := newHTTPClient(time.Second, caFile)
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	c := &client{server: server.URL, token: "npsu_test", http: httpClient, out: &bytes.Buffer{}}
	if err := c.get("/api/v1/overview"); err != nil {
		t.Fatalf("request with custom CA failed: %v", err)
	}
	if _, err := newHTTPClient(time.Second, t.TempDir()+"/missing.pem"); err == nil || !strings.Contains(err.Error(), "--ca-file") {
		t.Fatalf("expected missing CA file error, got %v", err)
	}
}

func TestRunRejectsConflictingOutputFlags(t *testing.T) {
	err := runWithWriters([]string{"--json", "--fields", "Id", "clients"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "不能同时使用") {
		t.Fatalf("expected conflicting output error, got %v", err)
	}
}

func TestParseFields(t *testing.T) {
	fields, err := parseFields(" id, Remark ")
	if err != nil {
		t.Fatalf("parseFields: %v", err)
	}
	if got, want := strings.Join(fields, ","), "id,Remark"; got != want {
		t.Fatalf("fields = %q, want %q", got, want)
	}
	for _, input := range []string{"", "id,,remark", "id,ID"} {
		if _, err := parseFields(input); err == nil {
			t.Fatalf("expected %q to fail", input)
		}
	}
}
