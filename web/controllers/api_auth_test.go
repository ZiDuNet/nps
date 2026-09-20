package controllers

import (
	"strings"
	"testing"
	"time"

	"ehang.io/nps/lib/file"
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
}
