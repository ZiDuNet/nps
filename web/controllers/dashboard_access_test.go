package controllers

import (
	"strings"
	"testing"
	"time"

	"ehang.io/nps/lib/file"
)

func TestDashboardAccessKeyGenerationAndValidation(t *testing.T) {
	key, hash, err := generateDashboardAccessKey("custom_key-1234567890")
	if err != nil {
		t.Fatalf("generateDashboardAccessKey: %v", err)
	}
	if key != dashboardAccessKeyPrefix+"custom_key-1234567890" || !validDashboardAccessKey(hash, key) {
		t.Fatalf("custom dashboard key did not validate: key=%q hash=%q", key, hash)
	}
	if strings.Contains(hash, key) || validDashboardAccessKey(hash, key+"x") {
		t.Fatal("dashboard key hash must not contain or accept the raw key")
	}
	if _, _, err := generateDashboardAccessKey("too-short"); err == nil {
		t.Fatal("short custom dashboard key was accepted")
	}
}

func TestLookupDashboardAccessKeyScopesAndRevokes(t *testing.T) {
	previous := file.GetDb()
	db := &file.DbUtils{JsonDb: file.NewJsonDb(t.TempDir())}
	file.Db = db
	t.Cleanup(func() { file.Db = previous })

	adminKey, adminHash, err := generateDashboardAccessKey("")
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	if err := db.SetGlobalDashboardKeyHash(adminHash); err != nil {
		t.Fatalf("store admin key: %v", err)
	}
	userKey, userHash, err := generateDashboardAccessKey("")
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	user := &file.User{Id: 7, UserName: "alice", Password: "secret", Status: true, DashboardKeyHash: userHash, ExpireTime: time.Now().Add(time.Hour).Format("2006-01-02 15:04:05")}
	db.JsonDb.Users.Store(user.Id, user)

	if principal, ok := lookupDashboardAccessKey(adminKey); !ok || !principal.Admin || principal.UserID != 0 {
		t.Fatalf("admin key lookup = %#v, %v", principal, ok)
	}
	if principal, ok := lookupDashboardAccessKey(userKey); !ok || principal.Admin || principal.UserID != user.Id {
		t.Fatalf("user key lookup = %#v, %v", principal, ok)
	}
	user.Lock()
	user.Status = false
	user.Unlock()
	if _, ok := lookupDashboardAccessKey(userKey); ok {
		t.Fatal("disabled user dashboard key remained valid")
	}
	user.Lock()
	user.Status = true
	user.ExpireTime = time.Now().Add(-time.Hour).Format("2006-01-02 15:04:05")
	user.Unlock()
	if _, ok := lookupDashboardAccessKey(userKey); ok {
		t.Fatal("expired user dashboard key remained valid")
	}
	if _, ok := lookupDashboardAccessKey(adminKey); !ok {
		t.Fatal("admin dashboard key should remain valid")
	}
}
