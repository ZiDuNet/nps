package file

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ehang.io/nps/lib/common"
)

func TestMigrateUsersFromClientsGroupsSameLegacyCredentials(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	clientA := &Client{
		Id:          1,
		VerifyKey:   "client-a",
		WebUserName: "alice",
		WebPassword: "secret",
	}
	clientB := &Client{
		Id:          2,
		VerifyKey:   "client-b",
		WebUserName: "alice",
		WebPassword: "secret",
	}
	db.Clients.Store(clientA.Id, clientA)
	db.Clients.Store(clientB.Id, clientB)

	utils := &DbUtils{JsonDb: db}
	if err := utils.MigrateUsersFromClients(); err != nil {
		t.Fatal(err)
	}

	user, err := utils.GetUserByName("alice")
	if err != nil {
		t.Fatal(err)
	}
	if user.Id == 0 {
		t.Fatal("expected migrated user to have an id")
	}
	if clientA.UserId != user.Id || clientB.UserId != user.Id {
		t.Fatalf("expected both clients to use user id %d, got %d and %d", user.Id, clientA.UserId, clientB.UserId)
	}
	if got := countUsers(db); got != 1 {
		t.Fatalf("expected one migrated user, got %d", got)
	}
}

func TestMigrateUsersFromClientsSplitsSameNameWithDifferentPasswords(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	clientA := &Client{
		Id:          1,
		VerifyKey:   "client-a",
		WebUserName: "alice",
		WebPassword: "secret-a",
	}
	clientB := &Client{
		Id:          2,
		VerifyKey:   "client-b",
		WebUserName: "alice",
		WebPassword: "secret-b",
	}
	db.Clients.Store(clientA.Id, clientA)
	db.Clients.Store(clientB.Id, clientB)

	utils := &DbUtils{JsonDb: db}
	if err := utils.MigrateUsersFromClients(); err != nil {
		t.Fatal(err)
	}

	if clientA.UserId == 0 || clientB.UserId == 0 {
		t.Fatalf("expected both clients to receive user ids, got %d and %d", clientA.UserId, clientB.UserId)
	}
	if clientA.UserId == clientB.UserId {
		t.Fatalf("expected conflicting legacy credentials to split users, got %d", clientA.UserId)
	}
	if got := countUsers(db); got != 2 {
		t.Fatalf("expected two migrated users, got %d", got)
	}
}

func TestMigrateUsersFromClientsRecoversMissingLegacyOwner(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	client := &Client{
		Id:          9,
		UserId:      42,
		VerifyKey:   "legacy-client",
		Status:      true,
		WebUserName: "legacy-user",
		WebPassword: "legacy-password",
	}
	db.Clients.Store(client.Id, client)

	utils := &DbUtils{JsonDb: db}
	if err := utils.MigrateUsersFromClients(); err != nil {
		t.Fatal(err)
	}
	user, err := utils.GetUser(client.UserId)
	if err != nil {
		t.Fatal(err)
	}
	if user.UserName != client.WebUserName || user.Password != client.WebPassword || !user.Status {
		t.Fatalf("legacy owner was not recovered: %#v", user)
	}
	if _, err := os.Stat(db.UserFilePath); err != nil {
		t.Fatalf("recovered users were not persisted: %v", err)
	}
	b, err := os.ReadFile(filepath.Clean(db.UserFilePath))
	if err != nil {
		t.Fatal(err)
	}
	var persisted User
	record := strings.Split(string(b), "\n"+common.CONN_DATA_SEQ)[0]
	if err := json.Unmarshal([]byte(record), &persisted); err != nil || persisted.Id != client.UserId {
		t.Fatalf("unexpected persisted users: %s", b)
	}
}

func TestMigrateUsersFromClientsDoesNotRecreateDeletedOwnerWhenFileExists(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	client := &Client{
		Id:          10,
		UserId:      43,
		VerifyKey:   "revoked-client",
		Status:      true,
		WebUserName: "revoked-user",
		WebPassword: "revoked-password",
	}
	db.Clients.Store(client.Id, client)
	if err := os.WriteFile(db.UserFilePath, []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}

	utils := &DbUtils{JsonDb: db}
	if err := utils.MigrateUsersFromClients(); err != nil {
		t.Fatal(err)
	}
	if _, err := utils.GetUser(client.UserId); err == nil {
		t.Fatal("a deleted owner was recreated even though users.json exists")
	}
}

func TestUserTunnelLimitCountsClientsTunnelsAndHosts(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	user := &User{Id: 1, UserName: "alice", Status: true, MaxTunnelNum: 2}
	clientA := &Client{Id: 1, UserId: user.Id, VerifyKey: "client-a", Status: true}
	clientB := &Client{Id: 2, UserId: user.Id, VerifyKey: "client-b", Status: true}
	otherClient := &Client{Id: 3, VerifyKey: "other", Status: true}
	db.Users.Store(user.Id, user)
	db.Clients.Store(clientA.Id, clientA)
	db.Clients.Store(clientB.Id, clientB)
	db.Clients.Store(otherClient.Id, otherClient)
	db.Tasks.Store(1, &Tunnel{Id: 1, Client: clientA})
	db.Hosts.Store(1, &Host{Id: 1, Client: clientB})
	db.Tasks.Store(2, &Tunnel{Id: 2, Client: otherClient})

	utils := &DbUtils{JsonDb: db}
	if !utils.IsUserTunnelLimitReached(user.Id) {
		t.Fatal("expected user tunnel limit to be reached")
	}

	user.MaxTunnelNum = 3
	if utils.IsUserTunnelLimitReached(user.Id) {
		t.Fatal("expected user tunnel limit not to be reached")
	}
}

func TestIsUserActiveSnapshotsState(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	active := &User{Id: 1, Status: true}
	expired := &User{Id: 2, Status: true, ExpireTime: time.Now().Add(-time.Minute).Format("2006-01-02 15:04:05")}
	disabled := &User{Id: 3, Status: false}
	malformed := &User{Id: 4, Status: true, ExpireTime: "not-a-date"}
	for _, user := range []*User{active, expired, disabled, malformed} {
		db.Users.Store(user.Id, user)
	}
	utils := &DbUtils{JsonDb: db}
	if !utils.IsUserActive(active.Id) {
		t.Fatal("active user without expiry should be active")
	}
	if utils.IsUserActive(expired.Id) {
		t.Fatal("expired user should not be active")
	}
	if utils.IsUserActive(disabled.Id) {
		t.Fatal("disabled user should not be active")
	}
	if utils.IsUserActive(malformed.Id) {
		t.Fatal("malformed expiry must fail closed")
	}
}

func TestGetIdByVerifyKeyRejectsExpiredOwningUser(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	user := &User{
		Id:         7,
		Status:     true,
		ExpireTime: time.Now().Add(-time.Minute).Format("2006-01-02 15:04:05"),
	}
	client := &Client{Id: 11, UserId: user.Id, VerifyKey: "client-secret", Status: true}
	disabledUser := &User{Id: 8, Status: false}
	disabledClient := &Client{Id: 12, UserId: disabledUser.Id, VerifyKey: "disabled-secret", Status: true}
	db.Users.Store(user.Id, user)
	db.Clients.Store(client.Id, client)
	db.Users.Store(disabledUser.Id, disabledUser)
	db.Clients.Store(disabledClient.Id, disabledClient)
	utils := &DbUtils{JsonDb: db}
	if _, err := utils.GetIdByVerifyKey(common.Getverifyval(client.VerifyKey), "192.0.2.10:1234"); err == nil {
		t.Fatal("expired user's client must not authenticate to the bridge")
	}
	if _, err := utils.GetIdByVerifyKey(common.Getverifyval(disabledClient.VerifyKey), "192.0.2.11:1234"); err == nil {
		t.Fatal("disabled user's client must not authenticate to the bridge")
	}

	user.Lock()
	user.ExpireTime = ""
	user.Unlock()
	id, err := utils.GetIdByVerifyKey(common.Getverifyval(client.VerifyKey), "192.0.2.10:1234")
	if err != nil || id != client.Id {
		t.Fatalf("active user's client should authenticate, id=%d err=%v", id, err)
	}
	client.RLock()
	addr := client.Addr
	client.RUnlock()
	if addr != "192.0.2.10" {
		t.Fatalf("client address was not normalized under lock: %q", addr)
	}
}

func TestIsClientActiveRequiresActiveOwner(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	active := &User{Id: 1, Status: true}
	disabled := &User{Id: 2, Status: false}
	db.Users.Store(active.Id, active)
	db.Users.Store(disabled.Id, disabled)
	utils := &DbUtils{JsonDb: db}

	if !utils.IsClientActive(&Client{Status: true}) {
		t.Fatal("unowned enabled client should remain active")
	}
	if !utils.IsClientActive(&Client{Status: true, UserId: active.Id}) {
		t.Fatal("client owned by active user should remain active")
	}
	if utils.IsClientActive(&Client{Status: true, UserId: disabled.Id}) {
		t.Fatal("client owned by disabled user must be inactive")
	}
	if utils.IsClientActive(&Client{Status: true, UserId: 999}) {
		t.Fatal("client with missing owner must fail closed")
	}
}

func TestDelUserDisablesOwnedClients(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	user := &User{Id: 5, UserName: "removed", Password: "secret", Status: true}
	owned := &Client{Id: 10, UserId: user.Id, Status: true, IsConnect: true}
	other := &Client{Id: 11, UserId: 6, Status: true}
	db.Users.Store(user.Id, user)
	db.Clients.Store(owned.Id, owned)
	db.Clients.Store(other.Id, other)
	utils := &DbUtils{JsonDb: db}

	if err := utils.DelUser(user.Id); err != nil {
		t.Fatal(err)
	}
	owned.RLock()
	ownedUserID, ownedStatus, ownedConnected := owned.UserId, owned.Status, owned.IsConnect
	owned.RUnlock()
	if ownedUserID != 0 || ownedStatus || ownedConnected {
		t.Fatalf("deleted user's client was not disabled: user=%d status=%v connected=%v", ownedUserID, ownedStatus, ownedConnected)
	}
	other.RLock()
	otherStatus := other.Status
	other.RUnlock()
	if !otherStatus {
		t.Fatal("deleting one user disabled an unrelated client")
	}
}

func countUsers(db *JsonDb) int {
	count := 0
	db.Users.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	return count
}
