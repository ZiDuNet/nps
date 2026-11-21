package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

func TestListKeepsRowsAndTotal(t *testing.T) {
	c, out := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":1,"code":"ok","msg":"ok","rows":[{"id":1,"remark":"a"}],"total":1,"request_id":"rid"}`))
	})
	if err := c.list("/api/v1/clients", 0); err != nil {
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
