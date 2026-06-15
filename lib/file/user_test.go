package file

import "testing"

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

func countUsers(db *JsonDb) int {
	count := 0
	db.Users.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	return count
}
