package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"ehang.io/nps/lib/file"
	"ehang.io/nps/web/audit"
	beego "github.com/astaxie/beego"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestMCPHandlerRejectsMissingBearer(t *testing.T) {
	handler := NewMCPHandler()
	req := httptest.NewRequest(http.MethodGet, "http://console.test/mcp", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without credentials, got %d", rr.Code)
	}
	if got := rr.Header().Get("WWW-Authenticate"); got == "" {
		t.Fatal("expected WWW-Authenticate challenge header")
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content type = %q, want JSON", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("401 body is not JSON: %v: %s", err, rr.Body.String())
	}
	if payload["code"] != "unauthorized" {
		t.Fatalf("401 payload = %#v", payload)
	}
}

func installMCPTestUserToken(t *testing.T, userID int) string {
	t.Helper()
	db := useClientPermissionTestDb(t)
	token, hash, err := generateUserAPIToken()
	if err != nil {
		t.Fatalf("generateUserAPIToken: %v", err)
	}
	db.JsonDb.Users.Store(userID, &file.User{
		Id: userID, UserName: "mcp-user", Status: true, APIKeyHash: hash,
		ExpireTime: time.Now().Add(24 * time.Hour).Format("2006-01-02 15:04:05"),
	})
	return token
}

func mcpPost(t *testing.T, endpoint, token string, payload map[string]any) (*http.Response, []byte) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw := new(bytes.Buffer)
	_, _ = raw.ReadFrom(resp.Body)
	return resp, raw.Bytes()
}

func TestMCPToolsDeclareExplicitSafetyAnnotations(t *testing.T) {
	s := server.NewMCPServer("nps-test", "1")
	registerMCPTools(s)
	tools := s.ListTools()

	readTool := tools["nps_clients"].Tool
	if readTool.Annotations.ReadOnlyHint == nil || !*readTool.Annotations.ReadOnlyHint ||
		readTool.Annotations.DestructiveHint == nil || *readTool.Annotations.DestructiveHint ||
		readTool.Annotations.OpenWorldHint == nil || *readTool.Annotations.OpenWorldHint {
		t.Fatalf("read tool annotations = %#v", readTool.Annotations)
	}
	createTool := tools["nps_client_create"].Tool
	if createTool.Annotations.ReadOnlyHint == nil || *createTool.Annotations.ReadOnlyHint ||
		createTool.Annotations.DestructiveHint == nil || *createTool.Annotations.DestructiveHint {
		t.Fatalf("create tool annotations = %#v", createTool.Annotations)
	}
	deleteTool := tools["nps_client_delete"].Tool
	if deleteTool.Annotations.DestructiveHint == nil || !*deleteTool.Annotations.DestructiveHint {
		t.Fatalf("delete tool annotations = %#v", deleteTool.Annotations)
	}
}

func TestMCPStatelessInitializeListAndCall(t *testing.T) {
	token := installMCPTestUserToken(t, 7)
	ts := httptest.NewServer(NewMCPHandler())
	t.Cleanup(ts.Close)

	initPayload := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": mcp.LATEST_PROTOCOL_VERSION,
			"clientInfo":      map[string]any{"name": "test", "version": "1"},
		},
	}
	resp, body := mcpPost(t, ts.URL, token, initPayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initialize status = %d: %s", resp.StatusCode, body)
	}
	if sessionID := resp.Header.Get("Mcp-Session-Id"); sessionID != "" {
		t.Fatalf("stateless MCP unexpectedly returned session id %q", sessionID)
	}

	resp, body = mcpPost(t, ts.URL, token, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"nps_dashboard"`)) {
		t.Fatalf("tools/list status = %d: %s", resp.StatusCode, body)
	}

	resp, body = mcpPost(t, ts.URL, token, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "nps_dashboard", "arguments": map[string]any{}},
	})
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`online_clients`)) {
		t.Fatalf("tools/call status = %d: %s", resp.StatusCode, body)
	}
}

func TestMCPGetStreamingIsDisabled(t *testing.T) {
	token := installMCPTestUserToken(t, 7)
	handler := NewMCPHandler()
	req := httptest.NewRequest(http.MethodGet, "http://console.test/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /mcp = %d, want 405: %s", rr.Code, rr.Body.String())
	}
}

func TestMCPAuditScopesUsersAndKeepsAdminGlobal(t *testing.T) {
	useClientPermissionTestDb(t)
	previousPath := beego.AppConfig.String("audit_log_path")
	auditPath := filepath.Join(t.TempDir(), "audit.log")
	_ = beego.AppConfig.Set("audit_log_path", auditPath)
	t.Cleanup(func() {
		_ = beego.AppConfig.Set("audit_log_path", previousPath)
		audit.Configure("")
	})
	audit.Configure(auditPath)
	now := time.Now()
	for _, event := range []audit.Event{
		{Time: now, ID: "owned", OwnerUserID: 7, Action: "client.create"},
		{Time: now.Add(time.Second), ID: "foreign", OwnerUserID: 8, Action: "client.delete"},
	} {
		if err := audit.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	request := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"limit": float64(10)}}}
	userCtx := context.WithValue(context.Background(), mcpPrincipalKey{}, &mcpPrincipal{UserID: 7})
	userResult, err := mcpAudit(userCtx, request)
	if err != nil || userResult.IsError {
		t.Fatalf("user audit result = %#v, err=%v", userResult, err)
	}
	userPage, _ := userResult.StructuredContent.(map[string]any)
	if userPage["total"] != 1 {
		t.Fatalf("ordinary user audit page = %#v", userPage)
	}

	adminCtx := context.WithValue(context.Background(), mcpPrincipalKey{}, &mcpPrincipal{IsAdmin: true})
	adminResult, err := mcpAudit(adminCtx, request)
	if err != nil || adminResult.IsError {
		t.Fatalf("admin audit result = %#v, err=%v", adminResult, err)
	}
	adminPage, _ := adminResult.StructuredContent.(map[string]any)
	if adminPage["total"] != 2 {
		t.Fatalf("admin audit page = %#v", adminPage)
	}
}

func TestMCPToolsRejectMissingPrincipal(t *testing.T) {
	result, err := mcpDashboard(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("missing principal result = %#v", result)
	}
}

func TestMCPClientsReturnRealTotalWithPagination(t *testing.T) {
	db := useClientPermissionTestDb(t)
	for _, id := range []int{1, 2, 3} {
		db.JsonDb.Clients.Store(id, &file.Client{Id: id, VerifyKey: "vkey", Status: true})
	}
	ctx := context.WithValue(context.Background(), mcpPrincipalKey{}, &mcpPrincipal{IsAdmin: true})
	request := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"offset": float64(1), "limit": float64(1),
	}}}
	result, err := mcpClients(ctx, request)
	if err != nil || result.IsError {
		t.Fatalf("clients result = %#v, err=%v", result, err)
	}
	page, _ := result.StructuredContent.(map[string]any)
	if page["total"] != 3 {
		t.Fatalf("total = %#v, want 3", page["total"])
	}
	clients, _ := page["clients"].([]mcpClientDTO)
	if len(clients) != 1 || clients[0].Id != 2 {
		t.Fatalf("page clients = %#v, want client 2", clients)
	}
}

func TestMCPClientRateLimitKeepsInt64Bps(t *testing.T) {
	_, _, rateLimitBps, err := mcpClientLimits(map[string]any{"rate_limit": 2147.483648})
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(math.MaxInt32) + 1; rateLimitBps != want {
		t.Fatalf("rate limit = %d bps, want %d", rateLimitBps, want)
	}
}
