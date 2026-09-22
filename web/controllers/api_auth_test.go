package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ehang.io/nps/lib/file"
	beecontext "github.com/astaxie/beego/context"
)

func TestUserAPITokenIsOpaqueAndRotatable(t *testing.T) {
	token, hash, err := generateUserAPIToken()
	if err != nil {
		t.Fatalf("generateUserAPIToken: %v", err)
	}
	if !strings.HasPrefix(token, userAPITokenPrefix) || len(hash) != 64 {
		t.Fatalf("unexpected API credential shape: token=%q hash=%q", token, hash)
	}
	if !validUserAPIToken(hash, token) {
		t.Fatal("generated token did not validate against its hash")
	}
	if validUserAPIToken(hash, token+"x") || validUserAPIToken("", token) {
		t.Fatal("invalid API token was accepted")
	}
}

func TestExtractUserAPIToken(t *testing.T) {
	if got := extractUserAPIToken("Bearer npsu_header", "npsu_query"); got != "npsu_header" {
		t.Fatalf("authorization header token = %q", got)
	}
	if got := extractUserAPIToken("", "npsu_query"); got != "npsu_query" {
		t.Fatalf("query token = %q", got)
	}
	if got := extractUserAPIToken("Basic abc", "npsu_query"); got != "npsu_query" {
		t.Fatalf("unsupported auth scheme should fall back to query token, got %q", got)
	}
}

func TestLookupUserByAPITokenRequiresActiveUser(t *testing.T) {
	previous := file.GetDb()
	utils := &file.DbUtils{JsonDb: file.NewJsonDb(t.TempDir())}
	file.Db = utils
	t.Cleanup(func() { file.Db = previous })

	token, hash, err := generateUserAPIToken()
	if err != nil {
		t.Fatalf("generateUserAPIToken: %v", err)
	}
	user := &file.User{
		Id:         42,
		UserName:   "api-user",
		Password:   "not-used-by-token-test",
		APIKeyHash: hash,
		Status:     true,
		ExpireTime: time.Now().Add(time.Hour).Format("2006-01-02 15:04:05"),
	}
	utils.JsonDb.Users.Store(user.Id, user)

	if got, ok := lookupUserByAPIToken(token); !ok || got != user.Id {
		t.Fatalf("active user token lookup = (%d, %v), want (%d, true)", got, ok, user.Id)
	}
	user.Lock()
	user.Status = false
	user.Unlock()
	if got, ok := lookupUserByAPIToken(token); ok || got != 0 {
		t.Fatalf("disabled user token lookup = (%d, %v), want (0, false)", got, ok)
	}
	user.Lock()
	user.Status = true
	user.ExpireTime = time.Now().Add(-time.Hour).Format("2006-01-02 15:04:05")
	user.Unlock()
	if got, ok := lookupUserByAPIToken(token); ok || got != 0 {
		t.Fatalf("expired user token lookup = (%d, %v), want (0, false)", got, ok)
	}
}

func newUserCredentialController(t *testing.T, action, query string, sessionValues map[interface{}]interface{}) (*UserController, *httptest.ResponseRecorder) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://console.test/user/"+strings.ToLower(action)+query, nil)
	request.Host = "console.test"
	request.Header.Set("Origin", "http://console.test")
	recorder := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(recorder, request)
	controller := &UserController{}
	controller.Init(ctx, "UserController", action, controller)
	session := &clientPermissionSession{values: sessionValues}
	controller.CruSession = session
	ctx.Input.CruSession = session
	return controller, recorder
}

func readUserCredentialResponse(t *testing.T, recorder *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var response map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode user credential response %q: %v", recorder.Body.String(), err)
	}
	return response
}

func TestUserAPIKeyHandlersEnforceOwnershipAndAdminManagement(t *testing.T) {
	db := useClientPermissionTestDb(t)
	for _, user := range []*file.User{
		{Id: 7, UserName: "alice", Password: "a", Status: true},
		{Id: 9, UserName: "bob", Password: "b", Status: true},
	} {
		db.JsonDb.Users.Store(user.Id, user)
	}

	ordinary, recorder := newUserCredentialController(t, "RegenerateAPIKey", "?id=9", testUserSession(7))
	runClientPermissionAction(t, ordinary.RegenerateAPIKey)
	if response := readUserCredentialResponse(t, recorder); response["status"] != float64(0) {
		t.Fatalf("ordinary user rotated another account's key: %#v", response)
	}
	if user, _ := db.GetUser(9); user != nil {
		user.RLock()
		stored := user.APIKeyHash
		user.RUnlock()
		if stored != "" {
			t.Fatalf("ordinary user changed another account's key: %q", stored)
		}
	}

	admin, recorder := newUserCredentialController(t, "RegenerateAPIKey", "?id=9", map[interface{}]interface{}{"auth": true, "isAdmin": true})
	runClientPermissionAction(t, admin.RegenerateAPIKey)
	response := readUserCredentialResponse(t, recorder)
	if response["status"] != float64(1) || response["token"] == "" {
		t.Fatalf("administrator could not rotate a user's key: %#v", response)
	}
	token, _ := response["token"].(string)
	if id, ok := lookupUserByAPIToken(token); !ok || id != 9 {
		t.Fatalf("administrator-issued token lookup = (%d, %v), want (9, true)", id, ok)
	}

	revoke, recorder := newUserCredentialController(t, "RevokeAPIKey", "?id=9", map[interface{}]interface{}{"auth": true, "isAdmin": true})
	runClientPermissionAction(t, revoke.RevokeAPIKey)
	if response := readUserCredentialResponse(t, recorder); response["status"] != float64(1) {
		t.Fatalf("administrator could not revoke a user's key: %#v", response)
	}
	if _, ok := lookupUserByAPIToken(token); ok {
		t.Fatal("revoked API token remained valid")
	}
}

func TestUserAPITokenRequestDoesNotCreateAdminSession(t *testing.T) {
	db := useClientPermissionTestDb(t)
	token, hash, err := generateUserAPIToken()
	if err != nil {
		t.Fatalf("generateUserAPIToken: %v", err)
	}
	db.JsonDb.Users.Store(7, &file.User{Id: 7, UserName: "alice", Password: "a", APIKeyHash: hash, Status: true})

	request := httptest.NewRequest(http.MethodPost, "http://console.test/user/regenerateapikey", nil)
	request.Host = "console.test"
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(recorder, request)
	controller := &UserController{}
	controller.Init(ctx, "UserController", "RegenerateAPIKey", controller)
	session := &clientPermissionSession{values: map[interface{}]interface{}{}}
	controller.CruSession = session
	ctx.Input.CruSession = session
	controller.Prepare()
	if controller.IsAdmin() {
		t.Fatal("ordinary API token was treated as an administrator session")
	}
	if session.Get("auth") != nil || session.Get("isAdmin") != nil {
		t.Fatalf("API authentication polluted browser session: %#v", session.values)
	}
	runClientPermissionAction(t, controller.RegenerateAPIKey)
	if response := readUserCredentialResponse(t, recorder); response["status"] != float64(1) {
		t.Fatalf("ordinary API token could not rotate its own key: %#v", response)
	}
}
