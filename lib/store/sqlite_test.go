package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSchemaVersionGuardRejectsNewerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nps.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SetMeta("schema_version", "999"); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("Open() error = %v, want ErrSchemaTooNew", err)
	}
}

func TestValidateMigratedRejectsTruncatedImport(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "nps.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ReplaceEntitiesWithMeta(map[string]map[int]json.RawMessage{}, map[string]string{
		"migrated_from": "json-gateway",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateMigrated(); err == nil {
		t.Fatal("truncated migrated database must be rejected")
	}
}

func TestReplaceEntitiesWithMetaIsAtomic(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "nps.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entities := map[string]map[int]json.RawMessage{
		KindClient: {1: json.RawMessage(`{"Id":1}`)},
	}
	if err := store.ReplaceEntitiesWithMeta(entities, map[string]string{"migrated_from": "json-gateway"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadEntities()
	if err != nil {
		t.Fatal(err)
	}
	if string(loaded[KindClient][1]) != `{"Id":1}` {
		t.Fatalf("client entity = %s", loaded[KindClient][1])
	}
	if migrated, err := store.Meta("migrated_from"); err != nil || migrated != "json-gateway" {
		t.Fatalf("meta migrated_from = %q, %v", migrated, err)
	}
}

func TestReplaceKindOnlyWritesChangedRows(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "nps.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := database.ReplaceKind(KindClient, map[int][]byte{
		1: []byte(`{"Id":1,"Value":"unchanged"}`),
		2: []byte(`{"Id":2,"Value":"removed"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`UPDATE entities SET updated_at = 123 WHERE kind = ? AND id = 1`, KindClient); err != nil {
		t.Fatal(err)
	}
	if err := database.ReplaceKind(KindClient, map[int][]byte{
		1: []byte(`{"Id":1,"Value":"unchanged"}`),
		3: []byte(`{"Id":3,"Value":"added"}`),
	}); err != nil {
		t.Fatal(err)
	}

	var updatedAt int64
	if err := database.db.QueryRow(`SELECT updated_at FROM entities WHERE kind = ? AND id = 1`, KindClient).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	if updatedAt != 123 {
		t.Fatalf("unchanged row updated_at = %d, want 123", updatedAt)
	}
	entities, err := database.LoadEntities()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := entities[KindClient][2]; exists {
		t.Fatal("removed row still exists after diff persistence")
	}
	if string(entities[KindClient][3]) != `{"Id":3,"Value":"added"}` {
		t.Fatalf("added row = %s", entities[KindClient][3])
	}
}

func TestReplaceKindsRollsBackAllKindsOnFailure(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "nps.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.db.Exec(`
		CREATE TRIGGER reject_user_insert
		BEFORE INSERT ON entities
		WHEN NEW.kind = 'user'
		BEGIN
			SELECT RAISE(ABORT, 'reject user');
		END
	`); err != nil {
		t.Fatal(err)
	}
	err = database.ReplaceKinds(map[string]map[int][]byte{
		KindClient: {1: []byte(`{"Id":1}`)},
		KindUser:   {1: []byte(`{"Id":1}`)},
	})
	if err == nil {
		t.Fatal("multi-kind transaction should fail on rejected user insert")
	}
	entities, loadErr := database.LoadEntities()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(entities) != 0 {
		t.Fatalf("failed multi-kind transaction committed partial data: %#v", entities)
	}
}

func TestSecondOpenIsRejectedByFileLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nps.db")
	first, created, err := OpenWithStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if !created {
		t.Fatal("first open should report that it created the database")
	}
	second, created, err := OpenWithStatus(path)
	if second != nil {
		_ = second.Close()
	}
	if !errors.Is(err, ErrDatabaseLocked) {
		t.Fatalf("second open error = %v, want ErrDatabaseLocked", err)
	}
	if created {
		t.Fatal("a lock failure must never report database ownership")
	}
}

func TestValidateMigratedAcceptsCompletedMigrationWithDeletedEntities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nps.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.ReplaceEntitiesWithMeta(nil, map[string]string{
		"migrated_from":       "json-gateway",
		"migration_completed": "1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.ValidateMigrated(); err != nil {
		t.Fatalf("completed migration with all entities deleted must be a valid empty database: %v", err)
	}
}

func TestValidateMigratedRejectsIncompleteMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nps.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.ReplaceEntitiesWithMeta(nil, map[string]string{
		"migrated_from": "json-gateway",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.ValidateMigrated(); err == nil {
		t.Fatal("migration metadata without a completion marker must be rejected")
	}
}

func TestDiscardIncompleteFilesRemovesDatabaseBeforeReleasingLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nps.db")
	database, created, err := OpenWithStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first open should create the database")
	}
	if err := database.DiscardIncompleteFiles(); err != nil {
		t.Fatal(err)
	}
	for _, current := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(current); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("discarded database left %s behind: %v", current, err)
		}
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatalf("reusable lock file should remain: %v", err)
	}
	reopened, created, err := OpenWithStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("reopen after discard should create a fresh database")
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFailureKeepsExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nps.db")
	database, created, err := OpenWithStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first open should create the database")
	}
	if _, err := database.db.Exec(`UPDATE meta SET value = ? WHERE key = 'schema_version'`, SchemaVersion+1); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, created, err := OpenWithStatus(path)
	if reopened != nil {
		_ = reopened.Close()
	}
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("reopen error = %v, want ErrSchemaTooNew", err)
	}
	if created {
		t.Fatal("existing database must not be reported as newly created")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("failed open removed the existing database: %v", err)
	}
}

func TestStoresAndSidecarsUsePrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}
	path := filepath.Join(t.TempDir(), "nps.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, current := range []string{path, path + ".lock", path + "-wal", path + "-shm"} {
		info, err := os.Stat(current)
		if err != nil {
			t.Fatalf("stat %s: %v", current, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s permissions = %o, want 600", current, got)
		}
	}
}
