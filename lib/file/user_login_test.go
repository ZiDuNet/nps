package file

import (
	"path/filepath"
	"testing"
)

func TestUserLoginLookupPreservesUsernamePrecedence(t *testing.T) {
	db := &DbUtils{JsonDb: NewJsonDb(t.TempDir())}
	alice := &User{Id: 1, UserName: "alice", Email: "alice@example.com", Password: "pass", Status: true}
	db.JsonDb.Users.Store(1, alice)
	for _, input := range []string{"alice", "ALICE@EXAMPLE.COM", " alice@example.com "} {
		if got, err := db.GetUserByLoginName(input); err != nil || got != alice {
			t.Fatalf("lookup %q: %v", input, err)
		}
	}
	legacy := &User{Id: 2, UserName: "alice@example.com", Password: "legacy"}
	db.JsonDb.Users.Store(2, legacy)
	if got, _ := db.GetUserByLoginName(legacy.UserName); got != legacy {
		t.Fatal("email alias stole existing username")
	}
	db.JsonDb.Users.Store(3, &User{Id: 3, Email: "ALICE@example.com"})
	if _, err := db.GetUserByEmail("alice@example.com"); err == nil {
		t.Fatal("ambiguous email randomly selected an account")
	}
	for _, invalid := range []string{"", "invalid", "Alice <alice@example.com>"} {
		if _, err := db.GetUserByEmail(invalid); err == nil {
			t.Fatalf("accepted invalid email identity: %q", invalid)
		}
	}
}

func TestResetUserPasswordRejectsChangedCredentialAndDisabledAccounts(t *testing.T) {
	db := &DbUtils{JsonDb: NewJsonDb(t.TempDir())}
	user := &User{Id: 1, UserName: "alice", Email: "alice@example.com", Password: "old", Status: true}
	db.JsonDb.Users.Store(1, user)
	for _, tc := range []struct{ email, previous string }{{"other@example.com", "old"}, {user.Email, "wrong"}} {
		if err := db.ResetUserPassword(1, tc.email, tc.previous, "new"); err == nil || user.Password != "old" {
			t.Fatal("password changed without matching account snapshot")
		}
	}
	user.Status = false
	if err := db.ResetUserPassword(1, user.Email, "old", "new"); err == nil {
		t.Fatal("disabled account recovered")
	}
}

func TestResetUserPasswordRollsBackWhenPersistenceFails(t *testing.T) {
	db := &DbUtils{JsonDb: NewJsonDb(t.TempDir())}
	user := &User{Id: 1, UserName: "alice", Email: "alice@example.com", Password: "old", Status: true}
	db.JsonDb.Users.Store(1, user)
	db.JsonDb.UserFilePath = filepath.Join(t.TempDir(), "nonexistent-directory", "users.json")
	if err := db.ResetUserPassword(1, user.Email, "old", "new"); err == nil || user.Password != "old" {
		t.Fatal("unsaved password change was reported as successful or retained in memory")
	}
}
