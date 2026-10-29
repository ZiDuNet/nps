package controllers

import "testing"

func TestFilterLoginBanRecords(t *testing.T) {
	rows := []loginBanRecord{
		{Key: "203.0.113.10", BanType: "ip", IsBanned: true, LastLoginTime: "2026-10-29 20:00:00"},
		{Key: "abc123", BanType: "username", IsBanned: false, LastLoginTime: "2026-10-29 19:00:00"},
		{Key: "198.51.100.8", BanType: "ip", IsBanned: false, LastLoginTime: "2026-10-28 18:00:00"},
	}

	filtered := filterLoginBanRecords(rows, "203.0", "ip", "banned")
	if len(filtered) != 1 || filtered[0].Key != "203.0.113.10" {
		t.Fatalf("filtered rows = %#v", filtered)
	}
	if filtered := filterLoginBanRecords(rows, "", "username", ""); len(filtered) != 1 || filtered[0].Key != "abc123" {
		t.Fatalf("username filter = %#v", filtered)
	}
	if filtered := filterLoginBanRecords(rows, "", "", "expired"); len(filtered) != 2 {
		t.Fatalf("expired filter = %#v", filtered)
	}
}
