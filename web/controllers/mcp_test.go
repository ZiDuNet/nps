package controllers

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
}
