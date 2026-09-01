package file

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ehang.io/nps/lib/common"
)

func TestLoadTaskFromJsonFileSkipsIncompleteRecords(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	db.TaskFilePath = filepath.Join(t.TempDir(), "tasks.json")
	db.Clients.Store(1, &Client{Id: 1})

	records := []string{
		`null`,
		`{"Id":1,"Client":{"Id":1}}`,
		`{"Id":2,"Client":{"Id":404},"Target":{"TargetStr":"127.0.0.1:80"}}`,
		`{"Id":3,"Client":{"Id":1},"Target":{"TargetStr":"127.0.0.1:80"}}`,
	}
	if err := os.WriteFile(db.TaskFilePath, []byte(strings.Join(records, "\n"+common.CONN_DATA_SEQ)), 0o600); err != nil {
		t.Fatal(err)
	}

	db.LoadTaskFromJsonFile()
	if _, ok := db.Tasks.Load(1); ok {
		t.Fatal("task without a target must be ignored")
	}
	if _, ok := db.Tasks.Load(2); ok {
		t.Fatal("task referencing an unknown client must be ignored")
	}
	v, ok := db.Tasks.Load(3)
	if !ok {
		t.Fatal("valid task was not restored")
	}
	task, ok := v.(*Tunnel)
	if !ok || task == nil || task.Client == nil || task.Target == nil || task.Flow == nil {
		t.Fatalf("restored task is incomplete: %#v", v)
	}
}

func TestLoadJsonFilesSkipNullRecords(t *testing.T) {
	dir := t.TempDir()
	db := NewJsonDb(dir)
	db.ClientFilePath = filepath.Join(dir, "clients.json")
	db.UserFilePath = filepath.Join(dir, "users.json")
	db.HostFilePath = filepath.Join(dir, "hosts.json")
	db.GlobalFilePath = filepath.Join(dir, "global.json")
	for _, path := range []string{db.ClientFilePath, db.UserFilePath, db.HostFilePath, db.GlobalFilePath} {
		if err := os.WriteFile(path, []byte(`null`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	db.LoadClientFromJsonFile()
	db.LoadUserFromJsonFile()
	db.LoadHostFromJsonFile()
	db.LoadGlobalFromJsonFile()
	if syncMapLen(&db.Clients) != 0 || syncMapLen(&db.Users) != 0 || syncMapLen(&db.Hosts) != 0 {
		t.Fatal("null records must not be stored")
	}
	if db.getGlobal() != nil {
		t.Fatal("null global record must not replace state")
	}
}

func TestLoadSyncMapFromFileSkipsUnreadablePath(t *testing.T) {
	dir := t.TempDir()
	called := false
	loadSyncMapFromFile(dir, func(string) { called = true })
	loadSyncMapFromFileWithSingleJson(dir, func(string) { called = true })
	if called {
		t.Fatal("callbacks must not run when the data path cannot be read")
	}
}

func syncMapLen(m interface {
	Range(func(key, value interface{}) bool)
}) int {
	count := 0
	m.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}
