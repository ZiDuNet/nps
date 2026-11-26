package controllers

import (
	"context"
	"strings"
	"testing"

	"ehang.io/nps/lib/file"
	"github.com/mark3labs/mcp-go/mcp"
)

func prepareMCPPlatformHostTest(t *testing.T) {
	t.Helper()
	db := useClientPermissionTestDb(t)
	db.JsonDb.Global = &file.Glob{PlatformDomains: []file.PlatformDomain{
		{ID: "pd-http", Wildcard: "*.example.com"},
	}}
	db.JsonDb.Clients.Store(1, &file.Client{
		Id:        1,
		VerifyKey: "mcp-host-vkey",
		Status:    true,
		Cnf:       &file.Config{},
	})
}

func TestMCPHostCreateUsesPlatformDomainAndRandomPrefix(t *testing.T) {
	prepareMCPPlatformHostTest(t)
	ctx := context.WithValue(context.Background(), mcpPrincipalKey{}, &mcpPrincipal{IsAdmin: true})
	request := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"client_id": float64(1),
		"scheme":    "http",
		"target":    "127.0.0.1:8080",
		"remark":    "mcp platform host",
	}}}

	result, err := mcpHostCreate(ctx, request)
	if err != nil || result.IsError {
		t.Fatalf("create result = %#v, err=%v", result, err)
	}
	payload, _ := result.StructuredContent.(map[string]any)
	host, _ := payload["host"].(string)
	if !strings.HasSuffix(host, ".example.com") {
		t.Fatalf("host = %q, want a platform hostname", host)
	}
	prefix := strings.TrimSuffix(host, ".example.com")
	if len(prefix) != 8 {
		t.Fatalf("prefix = %q, want 8 characters", prefix)
	}
	if payload["platform_domain_id"] != "pd-http" {
		t.Fatalf("platform_domain_id = %#v, want pd-http", payload["platform_domain_id"])
	}
}

func TestMCPHostCreateRejectsCustomDomainArgument(t *testing.T) {
	prepareMCPPlatformHostTest(t)
	ctx := context.WithValue(context.Background(), mcpPrincipalKey{}, &mcpPrincipal{IsAdmin: true})
	request := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"client_id": float64(1),
		"host":      "app.example.com",
		"target":    "127.0.0.1:8080",
	}}}

	result, err := mcpHostCreate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("custom domain result = %#v, want an error", result)
	}
}

func TestMCPPlatformDomainsOmitsCertificatePaths(t *testing.T) {
	prepareMCPPlatformHostTest(t)
	ctx := context.WithValue(context.Background(), mcpPrincipalKey{}, &mcpPrincipal{IsAdmin: true})

	result, err := mcpPlatformDomains(ctx, mcp.CallToolRequest{})
	if err != nil || result.IsError {
		t.Fatalf("platform domains result = %#v, err=%v", result, err)
	}
	payload, _ := result.StructuredContent.(map[string]any)
	var rows []any
	switch typed := payload["domains"].(type) {
	case []any:
		rows = typed
	case []map[string]any:
		for _, row := range typed {
			rows = append(rows, row)
		}
	}
	if len(rows) != 1 {
		t.Fatalf("domains = %#v, want one entry", payload["domains"])
	}
	domain, _ := rows[0].(map[string]any)
	if domain["id"] != "pd-http" || domain["wildcard"] != "*.example.com" {
		t.Fatalf("unexpected domain row: %#v", domain)
	}
	allowed := map[string]bool{"id": true, "wildcard": true, "certificate_configured": true}
	for key := range domain {
		if !allowed[strings.ToLower(key)] {
			t.Fatalf("domain row leaks unexpected field %q: %#v", key, domain)
		}
	}
}
