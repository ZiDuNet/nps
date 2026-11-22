package file

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/store"
	"ehang.io/nps/lib/version"
)

func seedGatewayRunPath(t *testing.T) string {
	t.Helper()
	runPath := t.TempDir()
	conf := filepath.Join(runPath, "conf")
	if err := os.MkdirAll(conf, 0o750); err != nil {
		t.Fatal(err)
	}
	records := map[string][]string{
		"clients.json": {
			`{"Id":1,"VerifyKey":"client-1","Status":true,"RateLimit":81920,"Flow":{"AccountingVersion":2}}`,
		},
		"users.json": {
			`{"Id":1,"UserName":"alice","Password":"secret","Status":true,"MaxTunnelNum":5}`,
		},
		"tasks.json": {
			`{"Id":1,"Mode":"tcp","Port":9000,"Status":true,"Client":{"Id":1},"Target":{"TargetStr":"127.0.0.1:80"},"Flow":{"AccountingVersion":2}}`,
		},
		"hosts.json": {
			`{"Id":1,"Host":"app.example.com","Location":"/","Scheme":"http","Client":{"Id":1},"Target":{"TargetStr":"127.0.0.1:80"},"Flow":{"AccountingVersion":2}}`,
		},
	}
	for name, rows := range records {
		if err := os.WriteFile(filepath.Join(conf, name), []byte(strings.Join(rows, "\n"+common.CONN_DATA_SEQ)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeJSONGatewayMarker(JSONGatewayMarkerPath(runPath), []string{
		gatewayFileClients, gatewayFileUsers, gatewayFileTasks, gatewayFileHosts,
	}); err != nil {
		t.Fatal(err)
	}
	return runPath
}

func trackStorageFixture(t *testing.T) {
	t.Helper()
	dbMu.RLock()
	previous := Db
	dbMu.RUnlock()
	t.Cleanup(func() {
		dbMu.Lock()
		current := Db
		Db = previous
		dbMu.Unlock()
		if current != nil && current != previous && current.JsonDb != nil && current.JsonDb.SQLite() != nil {
			stopSnapshotRates(current)
			_ = current.JsonDb.SQLite().Close()
		}
	})
}

func TestValidateJSONImportRejectsDanglingOwner(t *testing.T) {
	runPath := t.TempDir()
	conf := filepath.Join(runPath, "conf")
	if err := os.MkdirAll(conf, 0o750); err != nil {
		t.Fatal(err)
	}
	client := `{"Id":1,"VerifyKey":"client-1","Status":true,"UserId":42,"Flow":{"AccountingVersion":2}}`
	if err := os.WriteFile(filepath.Join(conf, "clients.json"), []byte(client), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := loadJSONImportSnapshot(runPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJSONImport(db); err == nil {
		t.Fatal("client referencing a missing user must fail import validation")
	}
}

func TestValidateJSONImportRejectsSkippedHostConflict(t *testing.T) {
	runPath := seedGatewayRunPath(t)
	conf := filepath.Join(runPath, "conf")
	// The host loader preserves same-owner overlaps but drops cross-tenant
	// conflicts; the latter must fail the import instead of disappearing.
	clientsPath := filepath.Join(conf, "clients.json")
	clients, err := os.ReadFile(clientsPath)
	if err != nil {
		t.Fatal(err)
	}
	secondClient := `{"Id":2,"VerifyKey":"client-2","Status":true,"UserId":1,"Flow":{"AccountingVersion":2}}`
	if err := os.WriteFile(clientsPath, append(clients, []byte("\n"+common.CONN_DATA_SEQ+secondClient)...), 0o600); err != nil {
		t.Fatal(err)
	}
	second := `{"Id":2,"Host":"app.example.com","Location":"/","Scheme":"http","Client":{"Id":2},"Target":{"TargetStr":"127.0.0.1:81"},"Flow":{"AccountingVersion":2}}`
	path := filepath.Join(conf, "hosts.json")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(content, []byte("\n"+common.CONN_DATA_SEQ+second)...), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := loadJSONImportSnapshot(runPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJSONImport(db); err == nil {
		t.Fatal("a route conflict must abort the import instead of dropping a host")
	}
}

func TestImportSQLiteFromJSONLeavesOriginalFilesOnValidationFailure(t *testing.T) {
	runPath := seedGatewayRunPath(t)
	conf := filepath.Join(runPath, "conf")
	clientPath := filepath.Join(conf, "clients.json")
	original, err := os.ReadFile(clientPath)
	if err != nil {
		t.Fatal(err)
	}
	// Malform the tasks file so validation must fail before any SQLite write.
	taskPath := filepath.Join(conf, "tasks.json")
	if err := os.WriteFile(taskPath, []byte(`{"Id":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ImportSQLiteFromJSON(runPath); err == nil {
		t.Fatal("import with malformed tasks.json must fail")
	}
	current, err := os.ReadFile(clientPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != string(original) {
		t.Fatal("failed import must leave the original JSON records untouched")
	}
	reportPath := filepath.Join(conf, "migration-report.json")
	reportData, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("failed import did not write a migration report: %v", err)
	}
	var report migrationReport
	if err := json.Unmarshal(reportData, &report); err != nil {
		t.Fatalf("migration report is not valid JSON: %v", err)
	}
	if report.Status != "failed" || report.Error == "" {
		t.Fatalf("migration report does not describe the failure: %#v", report)
	}
	for _, sensitive := range []string{"client-1", "secret"} {
		if strings.Contains(string(reportData), sensitive) {
			t.Fatalf("migration report leaked %q: %s", sensitive, reportData)
		}
	}
}

func TestInvalidLegacyMarkerDoesNotPublishDatabase(t *testing.T) {
	runPath := seedGatewayRunPath(t)
	conf := filepath.Join(runPath, "conf")
	if err := os.WriteFile(JSONGatewayMarkerPath(runPath), []byte(`{"schema":"nps-json-gateway-v1","version":"1.1.48"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := initializeStorage(runPath); err == nil {
		t.Fatal("ambiguous data with an old gateway marker must be rejected")
	}
	for _, name := range []string{"nps.db", "nps.db-wal", "nps.db-shm"} {
		if _, err := os.Stat(filepath.Join(conf, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rejected startup left %s behind (err=%v)", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(conf, "clients.json")); err != nil {
		t.Fatalf("rejected migration removed original client data: %v", err)
	}
}

func TestSuccessfulMigrationReportDoesNotWarnAboutArchivedUsersFile(t *testing.T) {
	runPath := seedGatewayRunPath(t)
	if err := ImportSQLiteFromJSON(runPath); err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(filepath.Join(runPath, "conf", "nps.db"))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := database.LoadMeta()
	_ = database.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		metaClientIDHighWater,
		metaUserIDHighWater,
		metaTaskIDHighWater,
		metaHostIDHighWater,
	} {
		if got := strings.TrimSpace(meta[key]); got != "1" {
			t.Fatalf("imported %s = %q, want 1", key, got)
		}
	}
	data, err := os.ReadFile(filepath.Join(runPath, "conf", "migration-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report migrationReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "success" {
		t.Fatalf("migration status = %q", report.Status)
	}
	for _, warning := range report.Warnings {
		if strings.Contains(warning, "未找到 users.json") {
			t.Fatalf("successful migration reported a false missing-users warning: %v", report.Warnings)
		}
	}
}

func TestPersistentIDHighWaterSurvivesEmptyDatabaseRestart(t *testing.T) {
	runPath := t.TempDir()
	conf := filepath.Join(runPath, "conf")
	if err := os.MkdirAll(conf, 0o750); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(conf, "nps.db")
	sqlite, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	jsonDb := NewJsonDb(runPath)
	jsonDb.SetSQLite(sqlite)
	jsonDb.ClientIncreaseId = 31
	jsonDb.UserIncreaseId = 17
	jsonDb.TaskIncreaseId = 83
	jsonDb.HostIncreaseId = 47
	if err := jsonDb.persistKinds(store.KindClient, store.KindUser, store.KindTunnel, store.KindHost); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loadedDB := &DbUtils{JsonDb: NewJsonDb(runPath)}
	if err := loadStorageFromSQLite(loadedDB, reopened); err != nil {
		t.Fatal(err)
	}
	if got := loadedDB.JsonDb.ClientIncreaseId; got != 31 {
		t.Fatalf("client high-water = %d, want 31", got)
	}
	if got := loadedDB.JsonDb.UserIncreaseId; got != 17 {
		t.Fatalf("user high-water = %d, want 17", got)
	}
	if got := loadedDB.JsonDb.TaskIncreaseId; got != 83 {
		t.Fatalf("tunnel high-water = %d, want 83", got)
	}
	if got := loadedDB.JsonDb.HostIncreaseId; got != 47 {
		t.Fatalf("host high-water = %d, want 47", got)
	}
}

func TestInitializeStorageRestoresHighWaterFromEmptyDatabase(t *testing.T) {
	runPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runPath, "conf"), 0o750); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(runPath, "conf", "nps.db")
	database, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.ReplaceKindsWithMeta(nil, map[string]string{
		metaClientIDHighWater: "23",
		metaUserIDHighWater:   "11",
		metaTaskIDHighWater:   "71",
		metaHostIDHighWater:   "37",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	// Stray historical JSON must not resurrect a deleted client even when
	// the existing authoritative SQLite database has no entities.
	writeGatewayTestRecords(t, filepath.Join(runPath, "conf", "clients.json"), `{"Id":7,"VerifyKey":"old-client"}`)

	trackStorageFixture(t)
	if err := initializeStorage(runPath); err != nil {
		t.Fatal(err)
	}
	dbMu.RLock()
	active := Db
	dbMu.RUnlock()
	if _, err := active.GetClient(7); err == nil {
		t.Fatal("existing empty SQLite database imported a residual client")
	}
	if _, err := os.Stat(filepath.Join(runPath, "conf", "clients.json")); err != nil {
		t.Fatalf("existing SQLite startup touched residual JSON: %v", err)
	}
	if got := active.JsonDb.ClientIncreaseId; got != 23 {
		t.Fatalf("client high-water through initializeStorage = %d, want 23", got)
	}
	if got := active.JsonDb.UserIncreaseId; got != 11 {
		t.Fatalf("user high-water through initializeStorage = %d, want 11", got)
	}
	if got := active.JsonDb.TaskIncreaseId; got != 71 {
		t.Fatalf("tunnel high-water through initializeStorage = %d, want 71", got)
	}
	if got := active.JsonDb.HostIncreaseId; got != 37 {
		t.Fatalf("host high-water through initializeStorage = %d, want 37", got)
	}
}

func TestDeletedMaximumIDIsNotReusedAfterRestart(t *testing.T) {
	runPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runPath, "conf"), 0o750); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(runPath, "conf", "nps.db")
	sqlite, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	jsonDb := NewJsonDb(runPath)
	jsonDb.SetSQLite(sqlite)
	client := &Client{Id: 1, VerifyKey: "high-water-client", Status: true}
	tunnel := &Tunnel{
		Id:     9,
		Mode:   "tcp",
		Port:   9000,
		Client: client,
		Target: &Target{TargetStr: "127.0.0.1:80"},
	}
	jsonDb.Clients.Store(client.Id, client)
	jsonDb.Tasks.Store(tunnel.Id, tunnel)
	jsonDb.ClientIncreaseId = 1
	jsonDb.TaskIncreaseId = 9
	if err := jsonDb.persistKinds(store.KindClient, store.KindTunnel); err != nil {
		t.Fatal(err)
	}
	db := &DbUtils{JsonDb: jsonDb}
	if err := db.DelTask(tunnel.Id); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loadedDB := &DbUtils{JsonDb: NewJsonDb(runPath)}
	if err := loadStorageFromSQLite(loadedDB, reopened); err != nil {
		t.Fatal(err)
	}
	if got := loadedDB.JsonDb.TaskIncreaseId; got != 9 {
		t.Fatalf("tunnel high-water after deleting max ID and restart = %d, want 9", got)
	}
}

func TestPersistentIDHighWaterRejectsMalformedMetadata(t *testing.T) {
	runPath := t.TempDir()
	conf := filepath.Join(runPath, "conf")
	if err := os.MkdirAll(conf, 0o750); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(conf, "nps.db")
	sqlite, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlite.ReplaceKindsWithMeta(nil, map[string]string{metaTaskIDHighWater: "not-a-number"}); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loadedDB := &DbUtils{JsonDb: NewJsonDb(runPath)}
	if err := loadStorageFromSQLite(loadedDB, reopened); err == nil {
		t.Fatal("malformed ID high-water metadata must stop startup")
	}
}

func TestSuccessfulImportKeepsDatabaseAfterArchiveFailure(t *testing.T) {
	runPath := seedGatewayRunPath(t)
	conf := filepath.Join(runPath, "conf")
	// Make the archive target a directory so renaming clients.json fails after
	// the SQLite transaction has already committed.
	blocked := filepath.Join(conf, "users.json.bak."+version.VERSION)
	if err := os.Mkdir(blocked, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := ImportSQLiteFromJSON(runPath); err == nil {
		t.Fatal("archive failure must be reported")
	}
	if _, err := os.Stat(filepath.Join(conf, "nps.db")); err != nil {
		t.Fatalf("successful SQLite import must remain after archive failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(conf, "clients.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clients.json should have been archived before the blocked users.json rename, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(conf, "users.json")); err != nil {
		t.Fatalf("blocked users.json rename must leave the source in place: %v", err)
	}
}

func TestExportSQLiteToJSONRefusesNotIncludedQuota(t *testing.T) {
	runPath := t.TempDir()
	conf := filepath.Join(runPath, "conf")
	if err := os.MkdirAll(conf, 0o750); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(conf, "nps.db"))
	if err != nil {
		t.Fatal(err)
	}
	user := map[string]any{"Id": 1, "UserName": "alice", "Password": "x", "Status": true, "MaxTunnelNum": 0}
	raw, _ := json.Marshal(user)
	if err := db.ReplaceEntities(map[string]map[int]json.RawMessage{
		store.KindUser: {1: raw},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "nested", "export")
	if err := ExportSQLiteToJSON(runPath, out); err == nil {
		t.Fatal("exporting a zero/not-included user quota must be refused")
	}
	data, err := os.ReadFile(filepath.Join(out, "migration-report.json"))
	if err != nil {
		t.Fatalf("failed export must leave a report: %v", err)
	}
	var report migrationReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "failed" || report.Direction != "sqlite_to_json" || !strings.Contains(report.Error, "套餐不包含") {
		t.Fatalf("failed export report = %+v", report)
	}
}

func TestResidualJSONDetectionNamesEveryRemainingSource(t *testing.T) {
	runPath := t.TempDir()
	conf := filepath.Join(runPath, "conf")
	if err := os.MkdirAll(conf, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"users.json", ".json-gateway"} {
		if err := os.WriteFile(filepath.Join(conf, name), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	residual := residualJSONFiles(runPath)
	if strings.Join(residual, ",") != "users.json,.json-gateway" {
		t.Fatalf("residual files = %v", residual)
	}
}

func TestExportSQLiteToJSONRoundTripsPositiveTinyLimit(t *testing.T) {
	runPath := t.TempDir()
	conf := filepath.Join(runPath, "conf")
	if err := os.MkdirAll(conf, 0o750); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(conf, "nps.db"))
	if err != nil {
		t.Fatal(err)
	}
	// 1024 bps is below one historical KB/s (8192 bps); export must round up
	// so the legacy reader does not treat it as unlimited.
	client := map[string]any{"Id": 1, "VerifyKey": "v", "Status": true, "RateLimit": 1024, "Flow": map[string]any{"AccountingVersion": 2}}
	raw, _ := json.Marshal(client)
	if err := db.ReplaceEntities(map[string]map[int]json.RawMessage{
		store.KindClient: {1: raw},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := ExportSQLiteToJSON(runPath, out); err != nil {
		t.Fatal(err)
	}
	loaded := NewJsonDb(out)
	loaded.LoadClientFromJsonFile()
	exported, err := loaded.GetClient(1)
	if err != nil {
		t.Fatal(err)
	}
	if exported.RateLimit != 1 {
		t.Fatalf("exported RateLimit = %d KB/s, want 1", exported.RateLimit)
	}
	if _, err := os.Stat(JSONGatewayMarkerPath(out)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy-unit export must not carry a canonical gateway marker: %v", err)
	}
	trackStorageFixture(t)
	if err := initializeStorage(out); err != nil {
		t.Fatalf("legacy-unit export should re-import automatically: %v", err)
	}
	if client, err := Db.GetClient(1); err != nil || client.RateLimit != 8192 {
		t.Fatalf("re-imported rate must be restored to bps: client=%v error=%v", client != nil, err)
	}
	data, err := os.ReadFile(filepath.Join(out, "migration-report.json"))
	if err != nil {
		t.Fatalf("successful export must leave a report: %v", err)
	}
	var report migrationReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.Schema != "nps-sqlite-export-v1" || report.Status != "success" ||
		report.Direction != "sqlite_to_json" || report.Counts.Clients != 1 {
		t.Fatalf("successful export report = %+v", report)
	}
}

func TestExportSQLiteToJSONRestoresManagedSettingsIntoNPSConfig(t *testing.T) {
	runPath := t.TempDir()
	conf := filepath.Join(runPath, "conf")
	if err := os.MkdirAll(conf, 0o750); err != nil {
		t.Fatal(err)
	}
	sourceConfig := "appname=nps\nbridge_port=8024\nallow_user_login=true\n"
	if err := os.WriteFile(filepath.Join(conf, "nps.conf"), []byte(sourceConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(filepath.Join(conf, "nps.db"))
	if err != nil {
		t.Fatal(err)
	}
	client := map[string]any{"Id": 1, "VerifyKey": "v", "Status": true}
	rawClient, _ := json.Marshal(client)
	if err := database.ReplaceEntities(map[string]map[int]json.RawMessage{
		store.KindClient: {1: rawClient},
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertSettings(map[string]string{
		"allow_user_login":     "false",
		"allow_user_register":  "true",
		"register_max_clients": "",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	outputDir := t.TempDir()
	if err := ExportSQLiteToJSON(runPath, outputDir); err != nil {
		t.Fatal(err)
	}
	exported, err := os.ReadFile(filepath.Join(outputDir, "conf", "nps.conf"))
	if err != nil {
		t.Fatal(err)
	}
	config := string(exported)
	for _, expected := range []string{
		"bridge_port=8024",
		"allow_user_login=false",
		"allow_user_register=true",
		"register_max_clients=",
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("exported nps.conf missing %q:\n%s", expected, config)
		}
	}
	if strings.Count(config, "allow_user_login=") != 1 {
		t.Fatalf("managed setting was duplicated:\n%s", config)
	}
	reportData, err := os.ReadFile(filepath.Join(outputDir, "migration-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report migrationReport
	if err := json.Unmarshal(reportData, &report); err != nil {
		t.Fatal(err)
	}
	if report.Counts.Settings != 3 {
		t.Fatalf("export settings count = %d, want 3", report.Counts.Settings)
	}
}

func TestLoadSQLiteRejectsCorruptAuthoritativeEntities(t *testing.T) {
	t.Run("invalid client JSON", func(t *testing.T) {
		db := NewJsonDb(t.TempDir())
		err := loadClientsFromEntities(db, map[int]json.RawMessage{1: json.RawMessage(`{`)})
		if err == nil {
			t.Fatal("corrupt authoritative client must fail closed")
		}
	})

	t.Run("tunnel with missing client", func(t *testing.T) {
		db := NewJsonDb(t.TempDir())
		tunnel, _ := json.Marshal(map[string]any{
			"Id":     1,
			"Client": map[string]any{"Id": 99},
			"Target": map[string]any{"TargetStr": "127.0.0.1:80"},
		})
		err := loadTunnelsFromEntities(db, map[int]json.RawMessage{1: tunnel})
		if err == nil {
			t.Fatal("tunnel referencing a missing authoritative client must fail closed")
		}
	})

	t.Run("invalid global platform domain", func(t *testing.T) {
		db := NewJsonDb(t.TempDir())
		global, _ := json.Marshal(map[string]any{
			"PlatformDomains": []map[string]any{{"ID": "bad", "Wildcard": "example.com"}},
		})
		err := loadGlobalFromEntity(db, global)
		if err == nil {
			t.Fatal("invalid authoritative platform domain must fail closed")
		}
	})

	t.Run("non-positive client key", func(t *testing.T) {
		db := NewJsonDb(t.TempDir())
		client, _ := json.Marshal(map[string]any{"Id": 0, "VerifyKey": "invalid-key"})
		if err := loadClientsFromEntities(db, map[int]json.RawMessage{0: client}); err == nil {
			t.Fatal("non-positive authoritative client key must fail closed")
		}
	})

	t.Run("runtime-only client", func(t *testing.T) {
		db := NewJsonDb(t.TempDir())
		client, _ := json.Marshal(map[string]any{"Id": 1, "VerifyKey": "runtime", "NoStore": true})
		if err := loadClientsFromEntities(db, map[int]json.RawMessage{1: client}); err == nil {
			t.Fatal("NoStore client in authoritative SQLite must fail closed")
		}
	})

	t.Run("runtime-only tunnel", func(t *testing.T) {
		db := NewJsonDb(t.TempDir())
		tunnel, _ := json.Marshal(map[string]any{
			"Id":      1,
			"NoStore": true,
			"Client":  map[string]any{"Id": 1},
			"Target":  map[string]any{"TargetStr": "127.0.0.1:80"},
		})
		if err := loadTunnelsFromEntities(db, map[int]json.RawMessage{1: tunnel}); err == nil {
			t.Fatal("NoStore tunnel in authoritative SQLite must fail closed")
		}
	})

	t.Run("global key must be zero", func(t *testing.T) {
		db := NewJsonDb(t.TempDir())
		global, _ := json.Marshal(map[string]any{"MaxConn": 10})
		if err := loadGlobalsFromEntities(db, map[int]json.RawMessage{7: global}); err == nil {
			t.Fatal("non-zero global entity key must fail closed")
		}
	})
}

func TestLoadSQLiteRepairsMissingLegacyClientConfig(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	client, _ := json.Marshal(map[string]any{
		"Id":        1,
		"VerifyKey": "legacy-null-config",
		"Status":    true,
		"Cnf":       nil,
	})
	if err := loadClientsFromEntities(db, map[int]json.RawMessage{1: client}); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.GetClient(1)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Cnf == nil {
		t.Fatal("legacy client without Cnf was not repaired")
	}
	if loaded.Cnf.Crypt || loaded.Cnf.Compress {
		t.Fatalf("default repaired config unexpectedly enabled wire transforms: %#v", loaded.Cnf)
	}
}
