package controllers

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWebSessionRegistryListValidateAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web_sessions.json")
	registry := newWebSessionRegistry(func() string { return path })
	now := time.Unix(1_800_000_000, 0)
	record := webSessionDeviceRecord{
		ID:         "device-a",
		Principal:  "user:7",
		Username:   "alice",
		UserID:     7,
		IP:         "192.0.2.10",
		UserAgent:  "Mozilla/5.0",
		CreatedAt:  now.Unix(),
		LastSeenAt: now.Unix(),
		ExpiresAt:  now.Add(time.Hour).Unix(),
		Epoch:      3,
	}
	if err := registry.Register(record); err != nil {
		t.Fatalf("register device: %v", err)
	}
	if !registry.Validate(record.ID, record.Principal, record.Epoch, now.Add(time.Minute)) {
		t.Fatal("registered device was not valid")
	}
	records, err := registry.List(record.Principal, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("list devices: %v", err)
	}
	if len(records) != 1 || records[0].ID != record.ID {
		t.Fatalf("listed devices = %#v", records)
	}

	reloaded := newWebSessionRegistry(func() string { return path })
	if !reloaded.Validate(record.ID, record.Principal, record.Epoch, now.Add(2*time.Minute)) {
		t.Fatal("persisted device was not valid after reload")
	}
}

func TestWebSessionRegistryRevokeAndRevokeAllInvalidateOldCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web_sessions.json")
	registry := newWebSessionRegistry(func() string { return path })
	now := time.Unix(1_800_000_000, 0)
	principal := "user:8"
	for _, id := range []string{"device-a", "device-b"} {
		if err := registry.Register(webSessionDeviceRecord{
			ID:         id,
			Principal:  principal,
			CreatedAt:  now.Unix(),
			LastSeenAt: now.Unix(),
			ExpiresAt:  now.Add(time.Hour).Unix(),
		}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}

	revoked, err := registry.Revoke(principal, "device-a")
	if err != nil || !revoked {
		t.Fatalf("revoke device = %v, %v", revoked, err)
	}
	if registry.Validate("device-a", principal, 0, now) {
		t.Fatal("revoked device remained valid")
	}
	if !registry.Validate("device-b", principal, 0, now) {
		t.Fatal("unrelated device was revoked")
	}

	count, epoch, err := registry.RevokeAll(principal, now)
	if err != nil {
		t.Fatalf("revoke all: %v", err)
	}
	if count != 1 || epoch != 1 {
		t.Fatalf("revoke all = count %d epoch %d, want 1 and 1", count, epoch)
	}
	if registry.SessionEpochValid(principal, 0) {
		t.Fatal("old session epoch remained valid")
	}
	if !registry.SessionEpochValid(principal, epoch) {
		t.Fatal("new session epoch was rejected")
	}
}

func TestWebSessionRegistryCleansExpiredDevices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web_sessions.json")
	registry := newWebSessionRegistry(func() string { return path })
	now := time.Unix(1_800_000_000, 0)
	if err := registry.Register(webSessionDeviceRecord{
		ID:         "expired",
		Principal:  "admin",
		CreatedAt:  now.Add(-2 * time.Hour).Unix(),
		LastSeenAt: now.Add(-2 * time.Hour).Unix(),
		ExpiresAt:  now.Add(-time.Minute).Unix(),
	}); err != nil {
		t.Fatalf("register expired device: %v", err)
	}
	records, err := registry.List("admin", now)
	if err != nil {
		t.Fatalf("list expired devices: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expired devices = %#v", records)
	}
}

func TestWebSessionRegistryFailsClosedWhenStoreIsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web_sessions.json")
	if err := os.WriteFile(path, []byte("{invalid"), 0600); err != nil {
		t.Fatalf("write invalid registry: %v", err)
	}
	registry := newWebSessionRegistry(func() string { return path })
	if registry.SessionEpochValid("admin", 0) {
		t.Fatal("invalid registry must not validate a session")
	}
}
