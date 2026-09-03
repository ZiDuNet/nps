package controllers

import (
	"bytes"
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSessionBoolAcceptsCommonSessionValues(t *testing.T) {
	tests := []struct {
		name  string
		value interface{}
		want  bool
	}{
		{name: "bool true", value: true, want: true},
		{name: "bool false", value: false, want: false},
		{name: "string true", value: "true", want: true},
		{name: "string one", value: "1", want: true},
		{name: "string zero", value: "0", want: false},
		{name: "numeric one", value: 1, want: true},
		{name: "numeric zero", value: 0, want: false},
		{name: "nil", value: nil, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sessionBool(test.value); got != test.want {
				t.Fatalf("sessionBool(%#v) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestIsAdminAuthorizedKeepsAPICredentialsRequestScoped(t *testing.T) {
	if !isAdminAuthorized(true, false) {
		t.Fatal("signed API authorization should be effective for its request")
	}
	if isAdminAuthorized(false, false) {
		t.Fatal("an absent API credential and non-admin session must not elevate access")
	}
	if !isAdminAuthorized(false, true) {
		t.Fatal("an admin session should remain effective")
	}
}

func TestSameOriginRequest(t *testing.T) {
	request := httptest.NewRequest("POST", "http://nps.example:8081/index/add", nil)
	request.Host = "nps.example:8081"
	request.Header.Set("Origin", "http://nps.example:8081")
	if !isSameOriginRequest(request) {
		t.Fatal("matching origin and host should be allowed")
	}

	request.Header.Set("Origin", "http://nps.example")
	if isSameOriginRequest(request) {
		t.Fatal("origin with a different port must be rejected")
	}

	request.Header.Set("Origin", "https://nps.example:8081")
	if isSameOriginRequest(request) {
		t.Fatal("origin with a different scheme must be rejected")
	}

	request.Header.Set("Origin", "https://nps.example:8081")
	request.Header.Set("X-Forwarded-Proto", "https")
	if !isSameOriginRequest(request) {
		t.Fatal("trusted proxy scheme should be used when checking origin")
	}
}

func TestAllowedClientIDsForUserUseCurrentMembership(t *testing.T) {
	stale := map[int]struct{}{1: {}}
	fresh := map[int]struct{}{2: {}, 3: {}}
	loadedFor := 0
	got := allowedClientIDsForPrincipal(sessionPrincipalUser, 7, 0, stale, func(userID int) map[int]struct{} {
		loadedFor = userID
		return fresh
	})
	if loadedFor != 7 {
		t.Fatalf("membership lookup used user %d, want 7", loadedFor)
	}
	if _, ok := got[1]; ok {
		t.Fatalf("stale session client membership was retained: %#v", got)
	}
	if _, ok := got[2]; !ok {
		t.Fatalf("fresh database membership was not used: %#v", got)
	}
}

func TestActiveNonAdminPrincipalRequiresExpectedIdentity(t *testing.T) {
	tests := []struct {
		name                     string
		principal                string
		userID, clientID         int
		userActive, clientActive bool
		want                     bool
	}{
		{name: "active user", principal: sessionPrincipalUser, userID: 7, userActive: true, want: true},
		{name: "disabled user", principal: sessionPrincipalUser, userID: 7, userActive: false, want: false},
		{name: "active client", principal: sessionPrincipalClient, clientID: 4, clientActive: true, want: true},
		{name: "disabled client", principal: sessionPrincipalClient, clientID: 4, clientActive: false, want: false},
		{name: "legacy session is invalid", principal: "", userID: 7, userActive: true, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := activeNonAdminPrincipal(test.principal, test.userID, test.clientID, test.userActive, test.clientActive); got != test.want {
				t.Fatalf("activeNonAdminPrincipal() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestClearAuthenticationSessionRemovesEveryIdentityValue(t *testing.T) {
	removed := make(map[string]bool)
	clearAuthenticationSession(func(key interface{}) {
		name, ok := key.(string)
		if !ok {
			t.Fatalf("unexpected session key type %T", key)
		}
		removed[name] = true
	})
	for _, key := range []string{"auth", "isAdmin", "clientId", "clientIds", "userId", "username", sessionPrincipalKey} {
		if !removed[key] {
			t.Fatalf("session key %q was not cleared", key)
		}
	}
}

func TestPublicLayoutHidesAdministratorNavigationForOrdinaryUsers(t *testing.T) {
	tpl := template.Must(template.ParseFiles("../views/public/layout.html"))
	data := map[string]interface{}{
		"isAdmin":           false,
		"web_base_url":      "",
		"version":           "test",
		"github_repository": "ZiDuNet/nps",
		"LayoutContent":     "",
	}
	var rendered bytes.Buffer
	if err := tpl.Execute(&rendered, data); err != nil {
		t.Fatalf("execute public layout: %v", err)
	}
	if strings.Contains(rendered.String(), "/global/index") {
		t.Fatal("ordinary user navigation must not contain the global settings link")
	}
	if !strings.Contains(rendered.String(), "https://github.com/ZiDuNet/nps/releases/latest") {
		t.Fatal("console layout must link the version indicator to the canonical release page")
	}

	data["isAdmin"] = true
	rendered.Reset()
	if err := tpl.Execute(&rendered, data); err != nil {
		t.Fatalf("execute administrator layout: %v", err)
	}
	if !strings.Contains(rendered.String(), "/global/index") {
		t.Fatal("administrator navigation must retain the global settings link")
	}
}
