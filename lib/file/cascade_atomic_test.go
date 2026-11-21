package file

import (
	"path/filepath"
	"sync"
	"testing"

	"ehang.io/nps/lib/acl"
	"ehang.io/nps/lib/store"
)

func TestDelClientCascadeRemovesOwnedResourcesAtomically(t *testing.T) {
	runPath := t.TempDir()
	database, err := store.Open(filepath.Join(runPath, "conf", "nps.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	jsonDB := NewJsonDb(runPath)
	jsonDB.SetSQLite(database)
	utils := &DbUtils{JsonDb: jsonDB}
	client := &Client{Id: 10, VerifyKey: "client-ten"}
	other := &Client{Id: 20, VerifyKey: "client-twenty"}
	ownedTask := &Tunnel{Id: 11, Client: client}
	otherTask := &Tunnel{Id: 21, Client: other}
	ownedHost := &Host{Id: 12, Client: client, Host: "owned.example.com"}
	otherHost := &Host{Id: 22, Client: other, Host: "other.example.com"}
	jsonDB.Clients.Store(client.Id, client)
	jsonDB.Clients.Store(other.Id, other)
	jsonDB.Tasks.Store(ownedTask.Id, ownedTask)
	jsonDB.Tasks.Store(otherTask.Id, otherTask)
	jsonDB.Hosts.Store(ownedHost.Id, ownedHost)
	jsonDB.Hosts.Store(otherHost.Id, otherHost)
	if err := jsonDB.persistKinds(store.KindClient, store.KindTunnel, store.KindHost); err != nil {
		t.Fatal(err)
	}

	if err := utils.DelClientCascade(client.Id); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		name   string
		values interface {
			Load(interface{}) (interface{}, bool)
		}
		id int
	}{
		{name: "client", values: &jsonDB.Clients, id: client.Id},
		{name: "tunnel", values: &jsonDB.Tasks, id: ownedTask.Id},
		{name: "host", values: &jsonDB.Hosts, id: ownedHost.Id},
	} {
		if _, exists := check.values.Load(check.id); exists {
			t.Fatalf("owned %s %d still exists after cascade delete", check.name, check.id)
		}
	}
	if _, exists := jsonDB.Clients.Load(other.Id); !exists {
		t.Fatal("unrelated client was removed")
	}
	if _, exists := jsonDB.Tasks.Load(otherTask.Id); !exists {
		t.Fatal("unrelated tunnel was removed")
	}
	if _, exists := jsonDB.Hosts.Load(otherHost.Id); !exists {
		t.Fatal("unrelated host was removed")
	}

	entities, err := database.LoadEntities()
	if err != nil {
		t.Fatal(err)
	}
	if len(entities[store.KindClient]) != 1 || len(entities[store.KindTunnel]) != 1 || len(entities[store.KindHost]) != 1 {
		t.Fatalf("unexpected persisted cascade result: %#v", entities)
	}
}

func TestDelUserCascadeRemovesAccountAndAllOwnedResources(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	utils := &DbUtils{JsonDb: db}
	user := &User{Id: 7, UserName: "tenant", Password: "secret", Status: true}
	client := &Client{Id: 10, UserId: user.Id, VerifyKey: "tenant-client"}
	other := &Client{Id: 20, UserId: 8, VerifyKey: "other-client"}
	db.Users.Store(user.Id, user)
	db.Clients.Store(client.Id, client)
	db.Clients.Store(other.Id, other)
	db.Tasks.Store(11, &Tunnel{Id: 11, Client: client})
	db.Tasks.Store(21, &Tunnel{Id: 21, Client: other})
	db.Hosts.Store(12, &Host{Id: 12, Client: client, Host: "tenant.example.com"})
	db.Hosts.Store(22, &Host{Id: 22, Client: other, Host: "other.example.com"})

	if err := utils.DelUserCascade(user.Id); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		name   string
		values *syncMapChecker
		id     int
	}{
		{name: "user", values: mapChecker(&db.Users), id: user.Id},
		{name: "client", values: mapChecker(&db.Clients), id: client.Id},
		{name: "tunnel", values: mapChecker(&db.Tasks), id: 11},
		{name: "host", values: mapChecker(&db.Hosts), id: 12},
	} {
		if _, exists := check.values.Load(check.id); exists {
			t.Fatalf("owned %s %d still exists after user cascade delete", check.name, check.id)
		}
	}
	for _, check := range []struct {
		name   string
		values *syncMapChecker
		id     int
	}{
		{name: "client", values: mapChecker(&db.Clients), id: other.Id},
		{name: "tunnel", values: mapChecker(&db.Tasks), id: 21},
		{name: "host", values: mapChecker(&db.Hosts), id: 22},
	} {
		if _, exists := check.values.Load(check.id); !exists {
			t.Fatalf("unrelated %s %d was removed", check.name, check.id)
		}
	}
}

func TestSaveGlobalRollsBackMemoryWhenPersistenceFails(t *testing.T) {
	runPath := t.TempDir()
	database, err := store.Open(filepath.Join(runPath, "conf", "nps.db"))
	if err != nil {
		t.Fatal(err)
	}
	jsonDB := NewJsonDb(runPath)
	jsonDB.SetSQLite(database)
	utils := &DbUtils{JsonDb: jsonDB}

	oldGlobal := &Glob{
		MaxConn:   10,
		RateLimit: 1_000_000,
		PlatformDomains: []PlatformDomain{{
			ID:           "platform",
			Wildcard:     "*.example.com",
			CertFilePath: "old.crt",
			KeyFilePath:  "old.key",
		}},
	}
	jsonDB.setGlobal(oldGlobal)
	host := &Host{
		Id:               1,
		PlatformDomainID: "platform",
		Host:             "demo.example.com",
		CertFilePath:     "old.crt",
		KeyFilePath:      "old.key",
	}
	jsonDB.Hosts.Store(host.Id, host)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	next := cloneGlobal(oldGlobal)
	next.MaxConn = 20
	next.RateLimit = 2_000_000
	next.PlatformDomains[0].CertFilePath = "new.crt"
	next.PlatformDomains[0].KeyFilePath = "new.key"
	if err := utils.SaveGlobal(next); err == nil {
		t.Fatal("SaveGlobal should report a closed SQLite store")
	}

	current := jsonDB.getGlobal()
	if current == nil || current.MaxConn != oldGlobal.MaxConn || current.RateLimit != oldGlobal.RateLimit {
		t.Fatalf("global settings were not rolled back: %#v", current)
	}
	host.RLock()
	certPath, keyPath := host.CertFilePath, host.KeyFilePath
	host.RUnlock()
	if certPath != "old.crt" || keyPath != "old.key" {
		t.Fatalf("host certificate paths were not rolled back: %s %s", certPath, keyPath)
	}
}

func TestDelClientCascadeHookRunsOnlyAfterCommit(t *testing.T) {
	runPath := t.TempDir()
	database, err := store.Open(filepath.Join(runPath, "conf", "nps.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	jsonDB := NewJsonDb(runPath)
	jsonDB.SetSQLite(database)
	utils := &DbUtils{JsonDb: jsonDB}
	client := &Client{Id: 10, VerifyKey: "hook-client"}
	task := &Tunnel{Id: 11, Client: client}
	jsonDB.Clients.Store(client.Id, client)
	jsonDB.Tasks.Store(task.Id, task)
	jsonDB.ClientIncreaseId = 10
	jsonDB.TaskIncreaseId = 11
	if err := jsonDB.persistKinds(store.KindClient, store.KindTunnel); err != nil {
		t.Fatal(err)
	}

	called := false
	hook := func(clients []*Client, tunnels []*Tunnel, _ []*Host) {
		called = true
		if len(clients) != 1 || clients[0] != client || len(tunnels) != 1 || tunnels[0] != task {
			t.Fatalf("unexpected committed cascade payload: clients=%v tunnels=%v", clients, tunnels)
		}
		if jsonDB.ClientIncreaseId != 10 || jsonDB.TaskIncreaseId != 11 {
			t.Fatalf("IDs were reclaimed before runtime cleanup: client=%d task=%d", jsonDB.ClientIncreaseId, jsonDB.TaskIncreaseId)
		}
		entities, loadErr := database.LoadEntities()
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if len(entities[store.KindClient]) != 0 || len(entities[store.KindTunnel]) != 0 {
			t.Fatalf("runtime hook ran before persistence committed: %#v", entities)
		}
	}
	if err := utils.DelClientCascadeWithHook(client.Id, hook); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("post-commit hook was not called")
	}
}

func TestDelClientCascadeHookSkippedWhenPersistenceFails(t *testing.T) {
	runPath := t.TempDir()
	database, err := store.Open(filepath.Join(runPath, "conf", "nps.db"))
	if err != nil {
		t.Fatal(err)
	}
	jsonDB := NewJsonDb(runPath)
	jsonDB.SetSQLite(database)
	utils := &DbUtils{JsonDb: jsonDB}
	client := &Client{Id: 10, VerifyKey: "failed-hook-client"}
	task := &Tunnel{Id: 11, Client: client}
	host := &Host{Id: 12, Client: client, Host: "hook.example.com"}
	jsonDB.Clients.Store(client.Id, client)
	jsonDB.Tasks.Store(task.Id, task)
	jsonDB.Hosts.Store(host.Id, host)
	if err := jsonDB.persistKinds(store.KindClient, store.KindTunnel, store.KindHost); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	called := false
	err = utils.DelClientCascadeWithHook(client.Id, func([]*Client, []*Tunnel, []*Host) {
		called = true
	})
	if err == nil {
		t.Fatal("cascade delete should report the closed database")
	}
	if called {
		t.Fatal("runtime cleanup must not run when the deletion did not commit")
	}
	for _, check := range []struct {
		name   string
		values interface {
			Load(interface{}) (interface{}, bool)
		}
		id int
	}{
		{name: "client", values: &jsonDB.Clients, id: client.Id},
		{name: "tunnel", values: &jsonDB.Tasks, id: task.Id},
		{name: "host", values: &jsonDB.Hosts, id: host.Id},
	} {
		if _, exists := check.values.Load(check.id); !exists {
			t.Fatalf("failed deletion did not restore %s %d", check.name, check.id)
		}
	}
}

func TestDelTaskHookRunsAfterCommitBeforeIDReclaim(t *testing.T) {
	runPath := t.TempDir()
	database, err := store.Open(filepath.Join(runPath, "conf", "nps.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	jsonDB := NewJsonDb(runPath)
	jsonDB.SetSQLite(database)
	utils := &DbUtils{JsonDb: jsonDB}
	task := &Tunnel{Id: 11, Mode: "tcp", Port: 9000, Target: &Target{TargetStr: "127.0.0.1:80"}}
	jsonDB.Tasks.Store(task.Id, task)
	jsonDB.TaskIncreaseId = 11
	if err := jsonDB.persistKinds(store.KindTunnel); err != nil {
		t.Fatal(err)
	}

	called := false
	err = utils.DelTaskWithHook(task.Id, func(deleted *Tunnel) error {
		called = true
		if deleted != task {
			t.Fatalf("deleted task = %v, want %v", deleted, task)
		}
		if jsonDB.TaskIncreaseId != 11 {
			t.Fatalf("task ID was reclaimed before runtime cleanup: %d", jsonDB.TaskIncreaseId)
		}
		entities, loadErr := database.LoadEntities()
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if len(entities[store.KindTunnel]) != 0 {
			t.Fatalf("runtime hook ran before persistence committed: %#v", entities[store.KindTunnel])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("post-commit hook was not called")
	}
}

func TestDelTaskHookSkippedWhenPersistenceFails(t *testing.T) {
	runPath := t.TempDir()
	database, err := store.Open(filepath.Join(runPath, "conf", "nps.db"))
	if err != nil {
		t.Fatal(err)
	}
	jsonDB := NewJsonDb(runPath)
	jsonDB.SetSQLite(database)
	utils := &DbUtils{JsonDb: jsonDB}
	task := &Tunnel{Id: 11, Mode: "tcp", Port: 9000, Target: &Target{TargetStr: "127.0.0.1:80"}}
	jsonDB.Tasks.Store(task.Id, task)
	if err := jsonDB.persistKinds(store.KindTunnel); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	called := false
	if err := utils.DelTaskWithHook(task.Id, func(*Tunnel) error {
		called = true
		return nil
	}); err == nil {
		t.Fatal("task delete should report the closed database")
	}
	if called {
		t.Fatal("runtime cleanup must not run when the deletion did not commit")
	}
	if _, exists := jsonDB.Tasks.Load(task.Id); !exists {
		t.Fatal("failed task deletion did not restore the record")
	}
}

func TestPersistentIDsAreMonotonicAndCannotOverwriteExistingRecords(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	utils := &DbUtils{JsonDb: db}
	client := &Client{Id: 1, VerifyKey: "monotonic-client", Status: true}
	if err := utils.NewClient(client); err != nil {
		t.Fatal(err)
	}
	first := &Tunnel{
		Id:     5,
		Mode:   "tcp",
		Port:   9000,
		Client: client,
		Target: &Target{TargetStr: "127.0.0.1:80"},
	}
	if err := utils.NewTask(first); err != nil {
		t.Fatal(err)
	}
	duplicate := &Tunnel{
		Id:     5,
		Mode:   "tcp",
		Port:   9001,
		Client: client,
		Target: &Target{TargetStr: "127.0.0.1:81"},
	}
	if err := utils.NewTask(duplicate); err == nil {
		t.Fatal("an explicit duplicate tunnel ID must be rejected")
	}
	if stored, err := utils.GetTask(first.Id); err != nil || stored != first {
		t.Fatal("duplicate ID overwrote the existing tunnel")
	}
	if db.TaskIncreaseId != 5 {
		t.Fatalf("explicit tunnel ID did not advance the counter: %d", db.TaskIncreaseId)
	}
	if err := utils.DelTask(first.Id); err != nil {
		t.Fatal(err)
	}
	next := &Tunnel{
		Mode:   "tcp",
		Port:   9002,
		Client: client,
		Target: &Target{TargetStr: "127.0.0.1:82"},
	}
	if err := utils.NewTask(next); err != nil {
		t.Fatal(err)
	}
	if next.Id != 6 {
		t.Fatalf("next tunnel ID = %d, want monotonic ID 6", next.Id)
	}
}

func TestCascadeDeleteRejectsStaleClientForNewResources(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	utils := &DbUtils{JsonDb: db}
	client := &Client{Id: 10, VerifyKey: "stale-client", Status: true}
	db.Clients.Store(client.Id, client)
	if err := utils.DelClientCascade(client.Id); err != nil {
		t.Fatal(err)
	}

	task := &Tunnel{
		Mode:   "tcp",
		Port:   9000,
		Client: client,
		Target: &Target{TargetStr: "127.0.0.1:80"},
	}
	if err := utils.NewTask(task); err == nil {
		t.Fatal("a tunnel must not be created from a deleted *Client reference")
	}
	host := &Host{
		Host:   "stale.example.com",
		Scheme: "http",
		Client: client,
		Target: &Target{TargetStr: "127.0.0.1:80"},
	}
	if err := utils.NewHost(host); err == nil {
		t.Fatal("a host must not be created from a deleted *Client reference")
	}
}

func TestMarshalStoredEntitySynchronizesNestedRuntimeFields(t *testing.T) {
	client := &Client{Id: 10, VerifyKey: "snapshot-client", Flow: NewFlow()}
	tunnel := &Tunnel{
		Id:     11,
		Client: client,
		Flow:   NewFlow(),
		Target: &Target{TargetStr: "127.0.0.1:80"},
	}
	host := &Host{
		Id:     12,
		Client: client,
		Flow:   NewFlow(),
		Target: &Target{TargetStr: "127.0.0.1:80"},
		Host:   "snapshot.example.com",
	}
	var workers sync.WaitGroup
	workers.Add(8)
	go func() {
		defer workers.Done()
		for i := 0; i < 500; i++ {
			if _, keep := marshalStoredEntity(tunnel); !keep {
				t.Error("active tunnel snapshot was dropped")
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		for i := 0; i < 500; i++ {
			tunnel.SetRateLimits(int64(i), int64(i+1))
		}
	}()
	go func() {
		defer workers.Done()
		for i := 0; i < 500; i++ {
			tunnel.Health.HealthLock()
			tunnel.Health.HealthCheckTimeout = i + 1
			tunnel.Health.HealthUnlock()
		}
	}()
	go func() {
		defer workers.Done()
		for i := 0; i < 500; i++ {
			if _, keep := marshalStoredEntity(host); !keep {
				t.Error("active host snapshot was dropped")
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		for i := 0; i < 500; i++ {
			host.SetRateLimits(int64(i), int64(i+1))
		}
	}()
	go func() {
		defer workers.Done()
		for i := 0; i < 500; i++ {
			tunnel.SetSourceLists(acl.DenyFirst, []string{"192.0.2.1"}, nil, acl.ScopeUser, "tunnel")
		}
	}()
	go func() {
		defer workers.Done()
		for i := 0; i < 500; i++ {
			host.SetSourceLists(acl.DenyFirst, nil, []string{"192.0.2.2"}, acl.ScopeUser, "host")
		}
	}()
	go func() {
		defer workers.Done()
		for i := 0; i < 500; i++ {
			client.Lock()
			client.RateLimit = int64(i + 1)
			client.Unlock()
		}
	}()
	workers.Wait()
}

type syncMapChecker struct {
	load func(interface{}) (interface{}, bool)
}

func (s *syncMapChecker) Load(key interface{}) (interface{}, bool) {
	return s.load(key)
}

func mapChecker(values interface {
	Load(interface{}) (interface{}, bool)
}) *syncMapChecker {
	return &syncMapChecker{load: values.Load}
}
