package file

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/version"
)

func writeGatewayTestRecords(t *testing.T, path string, records ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(records, "\n"+common.CONN_DATA_SEQ)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func copyBundledGatewaySamples(t *testing.T, runPath string) {
	t.Helper()
	confPath := filepath.Join(runPath, "conf")
	if err := os.MkdirAll(confPath, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		gatewayFileClients, gatewayFileTasks, gatewayFileHosts, gatewayFileGlobal,
	} {
		content, err := os.ReadFile(filepath.Join("..", "..", "conf", name))
		if err != nil {
			t.Fatalf("read bundled sample %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(confPath, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestJSONDataExistsIgnoresBundledSamplesAndEmptySkeletons(t *testing.T) {
	runPath := t.TempDir()
	copyBundledGatewaySamples(t, runPath)
	if JSONDataExists(runPath) {
		t.Fatal("repository sample data must not trigger the legacy JSON gate")
	}
	if err := EnsureGatewayForSQLite(runPath); err != nil {
		t.Fatalf("repository samples must be accepted as a clean install: %v", err)
	}

	for _, name := range []string{
		gatewayFileClients, gatewayFileUsers, gatewayFileTasks, gatewayFileHosts,
	} {
		writeGatewayTestRecords(t, filepath.Join(runPath, "conf", name), "", "null")
	}
	if err := os.WriteFile(filepath.Join(runPath, "conf", gatewayFileGlobal), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if JSONDataExists(runPath) {
		t.Fatal("empty record skeletons must not trigger the legacy JSON gate")
	}
}

func TestInitializeStorageIgnoresBundledSamples(t *testing.T) {
	runPath := t.TempDir()
	copyBundledGatewaySamples(t, runPath)
	if err := initializeStorage(runPath); err != nil {
		t.Fatalf("first SQLite startup with repository samples failed: %v", err)
	}
	if !common.FileExists(filepath.Join(runPath, "conf", "nps.db")) {
		t.Fatal("clean startup did not create the SQLite database")
	}
}

func TestJSONDataExistsStillMigratesRealLegacyData(t *testing.T) {
	runPath := t.TempDir()
	writeGatewayTestRecords(t, filepath.Join(runPath, "conf", gatewayFileClients),
		`{"Id":7,"VerifyKey":"legacy-real-client","Status":true,"RateLimit":10}`)
	if !JSONDataExists(runPath) {
		t.Fatal("real legacy data must still trigger migration")
	}
	if err := EnsureGatewayForSQLite(runPath); err == nil {
		t.Fatal("real legacy data without a gateway marker must be rejected")
	}
	if _, err := RunJSONGateway(runPath); err != nil {
		t.Fatalf("RunJSONGateway() = %v", err)
	}
	if err := EnsureGatewayForSQLite(runPath); err != nil {
		t.Fatalf("gateway must accept real legacy data after normalization: %v", err)
	}
	if err := ImportSQLiteFromJSON(runPath); err != nil {
		t.Fatalf("real legacy data was not migrated: %v", err)
	}
	if !common.FileExists(filepath.Join(runPath, "conf", "nps.db")) {
		t.Fatal("legacy data migration did not create the SQLite database")
	}
}

func TestJSONDataExistsRejectsPartiallyModifiedSamples(t *testing.T) {
	runPath := t.TempDir()
	copyBundledGatewaySamples(t, runPath)
	writeGatewayTestRecords(t, filepath.Join(runPath, "conf", gatewayFileUsers),
		`{"Id":1,"UserName":"real-user","Status":true}`)
	if !JSONDataExists(runPath) {
		t.Fatal("a real data file alongside repository samples must trigger migration")
	}
	err := initializeStorage(runPath)
	if err == nil {
		t.Fatal("startup must not ignore real data mixed with repository samples")
	}
	if !strings.Contains(err.Error(), "JSON 网关迁移标记") {
		t.Fatalf("unexpected startup error: %v", err)
	}
	if common.FileExists(filepath.Join(runPath, "conf", "nps.db")) {
		t.Fatal("gateway rejection must happen before SQLite is created")
	}
}

func TestRunJSONGatewayRejectsOldMarkerVersion(t *testing.T) {
	for _, oldVersion := range []string{"1.1.47", "1.1.48"} {
		t.Run(oldVersion, func(t *testing.T) {
			runPath := t.TempDir()
			writeGatewayTestRecords(t, filepath.Join(runPath, "conf", "clients.json"),
				`{"Id":1,"VerifyKey":"v","RateLimit":10}`)
			old := jsonGatewayMarker{
				Schema:  jsonGatewaySchema,
				Version: oldVersion,
				Files:   []string{gatewayFileClients},
			}
			content, _ := json.Marshal(old)
			if err := os.WriteFile(JSONGatewayMarkerPath(runPath), content, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := RunJSONGateway(runPath); err == nil {
				t.Fatalf("a gateway marker from %s must be rejected", oldVersion)
			}
		})
	}
}

func TestRunJSONGatewayDoesNotApplyConversionTwice(t *testing.T) {
	runPath := t.TempDir()
	clientsPath := filepath.Join(runPath, "conf", "clients.json")
	// A partial marker records clients.json as already normalized.
	writeGatewayTestRecords(t, clientsPath, `{"Id":1,"VerifyKey":"v","RateLimit":81920}`)
	partial := jsonGatewayMarker{
		Schema:  jsonGatewaySchema,
		Version: jsonGatewayMinVersion,
		Files:   []string{gatewayFileClients},
	}
	content, _ := json.Marshal(partial)
	if err := os.WriteFile(JSONGatewayMarkerPath(runPath), content, 0o600); err != nil {
		t.Fatal(err)
	}
	writeGatewayTestRecords(t, filepath.Join(runPath, "conf", "tasks.json"),
		`{"Id":1,"Mode":"tcp","Port":9000,"Target":{"TargetStr":"127.0.0.1:80"}}`)
	if _, err := RunJSONGateway(runPath); err != nil {
		t.Fatalf("RunJSONGateway() = %v", err)
	}
	loaded := NewJsonDb(runPath)
	loaded.LoadClientFromJsonFile()
	client, err := loaded.GetClient(1)
	if err != nil {
		t.Fatal(err)
	}
	if client.RateLimit != 81920 {
		t.Fatalf("RateLimit = %d, want 81920 (no double conversion)", client.RateLimit)
	}
	marker, err := ReadJSONGatewayMarker(runPath)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(marker.Files, gatewayFileTasks) {
		t.Fatalf("marker files = %v, want tasks.json processed", marker.Files)
	}
}

func TestRunJSONGatewayRecoversWhenMarkerIsLostAfterRewrite(t *testing.T) {
	runPath := t.TempDir()
	clientsPath := filepath.Join(runPath, "conf", gatewayFileClients)
	writeGatewayTestRecords(t, clientsPath, `{"Id":1,"VerifyKey":"v","RateLimit":10}`)

	if _, err := RunJSONGateway(runPath); err != nil {
		t.Fatalf("first RunJSONGateway() = %v", err)
	}
	backupPath := clientsPath + ".pre-gateway.bak." + version.VERSION
	backupBefore, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(JSONGatewayMarkerPath(runPath)); err != nil {
		t.Fatal(err)
	}
	if _, err := RunJSONGateway(runPath); err != nil {
		t.Fatalf("retry RunJSONGateway() = %v", err)
	}

	loaded := NewJsonDb(runPath)
	loaded.LoadClientFromJsonFile()
	client, err := loaded.GetClient(1)
	if err != nil {
		t.Fatal(err)
	}
	if client.RateLimit != 81920 {
		t.Fatalf("RateLimit = %d, want 81920 after marker loss", client.RateLimit)
	}
	backupAfter, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(backupAfter) != string(backupBefore) {
		t.Fatal("retry overwrote the original pre-gateway backup")
	}
}

func TestRunJSONGatewayRejectsMalformedRecord(t *testing.T) {
	runPath := t.TempDir()
	writeGatewayTestRecords(t, filepath.Join(runPath, "conf", "tasks.json"), `{"Id":1,`)
	if _, err := RunJSONGateway(runPath); err == nil {
		t.Fatal("malformed record must fail the gateway instead of being dropped")
	}
}

func TestRunJSONGatewayRejectsMalformedUserRecord(t *testing.T) {
	runPath := t.TempDir()
	writeGatewayTestRecords(t, filepath.Join(runPath, "conf", "users.json"), `{"Id":1,`)
	if _, err := RunJSONGateway(runPath); err == nil {
		t.Fatal("a malformed user record must fail instead of disappearing during migration")
	}
}

func TestHasJSONGatewayMarkerRequiresAllPresentFiles(t *testing.T) {
	runPath := t.TempDir()
	writeGatewayTestRecords(t, filepath.Join(runPath, "conf", "clients.json"),
		`{"Id":1,"VerifyKey":"v","RateLimit":10}`)
	writeGatewayTestRecords(t, filepath.Join(runPath, "conf", "tasks.json"),
		`{"Id":1,"Mode":"tcp","Port":9000,"Target":{"TargetStr":"127.0.0.1:80"}}`)
	partial := jsonGatewayMarker{
		Schema:  jsonGatewaySchema,
		Version: jsonGatewayMinVersion,
		Files:   []string{gatewayFileClients},
	}
	content, _ := json.Marshal(partial)
	if err := os.WriteFile(JSONGatewayMarkerPath(runPath), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if HasJSONGatewayMarker(runPath) {
		t.Fatal("a partial marker must not be reported as complete")
	}
}

func TestRunJSONGatewayRecordsAbsentFilesOnRetry(t *testing.T) {
	runPath := t.TempDir()
	writeGatewayTestRecords(t, filepath.Join(runPath, "conf", "clients.json"),
		`{"Id":1,"VerifyKey":"v","RateLimit":10}`)
	if _, err := RunJSONGateway(runPath); err != nil {
		t.Fatal(err)
	}
	marker, err := ReadJSONGatewayMarker(runPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{gatewayFileClients} {
		if !containsString(marker.Files, name) {
			t.Fatalf("marker files = %v, want %s processed", marker.Files, name)
		}
	}
	// A second run must be a no-op and keep the existing marker.
	result, err := RunJSONGateway(runPath)
	if err != nil {
		t.Fatal(err)
	}
	if result.Clients != 0 {
		t.Fatalf("second gateway pass reprocessed %d clients", result.Clients)
	}
}
