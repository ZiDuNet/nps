package controllers

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"ehang.io/nps/lib/file"
)

func TestUserListRowsRedactPasswords(t *testing.T) {
	rows := newUserListRows([]*file.User{
		{
			Id:       7,
			UserName: "alice",
			Password: "do-not-expose",
			Status:   true,
			Remark:   "<script>alert(1)</script>",
		},
	})

	encoded, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal user list rows: %v", err)
	}
	got := string(encoded)
	if strings.Contains(got, "do-not-expose") {
		t.Fatalf("user list response exposed a password: %s", got)
	}
	if !strings.Contains(got, `"Password":""`) {
		t.Fatalf("user list response no longer preserves the password field shape: %s", got)
	}
	var roundTrip []userListRow
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("unmarshal user list rows: %v", err)
	}
	if len(roundTrip) != 1 || roundTrip[0].Remark != "<script>alert(1)</script>" {
		t.Fatalf("expected list row to keep raw display text for template-level escaping, got %#v", roundTrip)
	}
}

func TestNewUserUpdateCandidateKeepsExistingPasswordWhenBlank(t *testing.T) {
	existing := &file.User{
		Id:           7,
		UserName:     "alice",
		Password:     "existing-password",
		Status:       true,
		Remark:       "before",
		MaxTunnelNum: 3,
		ExpireTime:   "2026-01-01 00:00:00",
		CreateTime:   "2025-01-01 00:00:00",
	}

	updated := newUserUpdateCandidate(existing, "alice-updated", "", "after", 6, "2027-01-01 00:00:00")
	if updated == existing {
		t.Fatal("update candidate must not mutate the stored user in place")
	}
	if updated.Password != "existing-password" {
		t.Fatalf("blank password should preserve the existing credential, got %q", updated.Password)
	}
	if existing.UserName != "alice" || existing.Remark != "before" || existing.MaxTunnelNum != 3 {
		t.Fatalf("building an update candidate mutated the existing user: %#v", existing)
	}

	updated = newUserUpdateCandidate(existing, "alice", "replacement<password>&", "before", 3, existing.ExpireTime)
	if updated.Password != "replacement<password>&" {
		t.Fatalf("supplied password should replace the credential, got %q", updated.Password)
	}
}

func TestNormalizeUserInput(t *testing.T) {
	if got := normalizeUserTunnelLimit(-1); got != 0 {
		t.Fatalf("negative tunnel limit should become unlimited, got %d", got)
	}
	if got := normalizeUserTunnelLimit(4); got != 4 {
		t.Fatalf("positive tunnel limit changed unexpectedly, got %d", got)
	}

	valid, err := normalizeUserExpireTime("2026-12-31 23:59")
	if err != nil || valid != "2026-12-31 23:59:00" {
		t.Fatalf("valid expiration was not normalized: value=%q err=%v", valid, err)
	}
	if _, err := normalizeUserExpireTime("not-a-date"); err == nil {
		t.Fatal("invalid expiration should not silently remove the limit")
	}
	if empty, err := normalizeUserExpireTime("   "); err != nil || empty != "" {
		t.Fatalf("empty expiration should mean no expiry: value=%q err=%v", empty, err)
	}
}

func TestParseUserStatusAcceptsFormAndBooleanValues(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: "0", want: false},
		{value: "1", want: true},
		{value: "false", want: false},
		{value: "true", want: true},
	} {
		got, err := parseUserStatus(test.value)
		if err != nil || got != test.want {
			t.Fatalf("parseUserStatus(%q) = %v, %v; want %v, nil", test.value, got, err, test.want)
		}
	}
	if _, err := parseUserStatus("enabled"); err == nil {
		t.Fatal("invalid status should return an error")
	}
}

func TestUserFormTemplatesProtectPasswords(t *testing.T) {
	add := readUserTemplateForTest(t, "../views/user/add.html")
	for _, expected := range []string{
		`type="password" name="password" autocomplete="new-password"`,
		`id="user-password-hint"`,
		`$('#user-form').on('submit'`,
	} {
		if !strings.Contains(add, expected) {
			t.Fatalf("add template misses password protection marker: %s", expected)
		}
	}

	edit := readUserTemplateForTest(t, "../views/user/edit.html")
	for _, expected := range []string{
		`type="password" name="password" autocomplete="new-password"`,
		`placeholder="留空则保持原密码"`,
		`现有密码不会显示在页面中`,
		`$('#user-form').on('submit'`,
	} {
		if !strings.Contains(edit, expected) {
			t.Fatalf("edit template misses password protection marker: %s", expected)
		}
	}
	if strings.Contains(edit, "{{.u.Password}}") {
		t.Fatal("edit template must not render the stored password")
	}
}

func TestUserListTemplateLocalizesUnlimitedValues(t *testing.T) {
	content := readUserTemplateForTest(t, "../views/user/list.html")
	for _, marker := range []string{
		`userUnlimitedText()`,
		`return npsIsEnglish() ? 'Unlimited' : '不限';`,
	} {
		if !strings.Contains(content, marker) {
			t.Fatalf("user list misses localized unlimited marker: %s", marker)
		}
	}
}

func readUserTemplateForTest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read template %s: %v", path, err)
	}
	return string(b)
}
