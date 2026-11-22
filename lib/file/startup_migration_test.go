package file

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/rate"
	"ehang.io/nps/lib/settings"
	"ehang.io/nps/lib/store"
	"github.com/astaxie/beego"
	_ "modernc.org/sqlite"
)

const startupMigrationBootConfig = `# Listener configuration
bridge_ip=127.0.0.1
bridge_port=8024
web_port=30080
bridge_cert_file=certs/bridge.pem
log_path=nps.log
custom_key=keep

`

func startupMigrationFixture(t *testing.T) (string, map[string]string) {
	t.Helper()
	t.Setenv("BEEGO_RUNMODE", "dev")
	runPath := t.TempDir()
	previousDB := Db
	previousSettings := make(map[string]string)
	for key := range settings.ManagedKeySet() {
		previousSettings[key] = beego.AppConfig.String(key)
	}
	previousRate := rate.GlobalLimit()
	_, previousMaxConn := GlobalConnections()
	t.Cleanup(func() {
		if Db != previousDB {
			if Db != nil && Db.JsonDb != nil && Db.JsonDb.SQLite() != nil {
				stopSnapshotRates(Db)
				_ = Db.JsonDb.SQLite().Close()
			}
			SetDbForTest(previousDB)
		}
		settings.ResetForTest()
		for key, value := range previousSettings {
			_ = beego.AppConfig.Set(key, value)
		}
		CleanupUserRuntime(731)
		rate.SetGlobalLimit(previousRate)
		SetGlobalMaxConn(int(previousMaxConn))
	})

	files := map[string]string{
		"nps.conf": startupMigrationBootConfig + `# Runtime business settings
allow_user_login=false
smtp_from="sender@example.test"
register_max_conn=0
allow_flow_limit=true
`,
		"clients.json": `{"Id":12,"VerifyKey":"legacy-client","Status":true,"UserId":731,"RateLimit":10}` +
			"\n" + common.CONN_DATA_SEQ +
			`{"Id":13,"VerifyKey":"converted-client","Status":true,"UserId":731,"RateLimit":81920,"_NPSGatewayVersion":"1.1.49"}`,
		"users.json":                          `{"Id":731,"UserName":"fixture-owner","Status":true,"MaxConn":0,"UploadLimit":2,"DownloadLimit":3}`,
		"tasks.json":                          `{"Id":14,"Mode":"tcp","Port":9000,"Status":true,"Client":{"Id":12},"Target":{"TargetStr":"127.0.0.1:80"},"UploadLimit":7,"DownloadLimit":8}`,
		"hosts.json":                          `{"Id":15,"Host":"app.example.test","Location":"/","Scheme":"http","Client":{"Id":13},"Target":{"TargetStr":"127.0.0.1:80"}}`,
		"global.json":                         `{"RateLimit":20,"MaxConn":17}`,
		"clients.json.pre-gateway.bak.1.1.49": "previous gateway artifact",
		"nps.conf.pre-settings.bak.1.1.50.20261121-010203": "previous settings artifact",
		"tasks.json.bak.manual":                            "operator copy",
		"instance.key":                                     "fixture key",
		"certs/bridge.pem":                                 "fixture certificate",
		"usage-history.json":                               "fixture usage history",
		"audit.log":                                        "fixture audit history",
	}
	if err := os.MkdirAll(filepath.Join(runPath, "conf", "certs"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		writeStartupMigrationFixture(t, runPath, name, content)
	}
	return runPath, files
}

func writeStartupMigrationFixture(t *testing.T, runPath, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(runPath, "conf", name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func startupMigrationRead(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func startupMigrationReport(t *testing.T, runPath string) migrationReport {
	t.Helper()
	var report migrationReport
	if err := json.Unmarshal(startupMigrationRead(t, filepath.Join(runPath, "conf", "migration-report.json")), &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func verifyStartupMigrationBackup(t *testing.T, runPath string, report migrationReport, original map[string]string) []byte {
	t.Helper()
	backupDir := filepath.Join(runPath, "conf", "backups")
	archives, err := filepath.Glob(filepath.Join(backupDir, "*.tar.gz"))
	if err != nil || !slices.Equal(archives, []string{report.BackupArchive}) {
		t.Fatalf("unexpected backup files: %v, %v", archives, err)
	}
	compressed := startupMigrationRead(t, report.BackupArchive)
	digest := sha256.Sum256(compressed)
	if hex.EncodeToString(digest[:]) != report.BackupSHA256 {
		t.Fatal("archive digest does not match the migration report")
	}
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	archived := make(map[string]string)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimPrefix(header.Name, "conf/")
		if name == header.Name || original[name] != string(content) {
			t.Fatalf("backup entry %q does not match the original", header.Name)
		}
		archived[name] = string(content)
	}
	if !maps.Equal(archived, original) {
		t.Fatal("backup does not contain the complete original configuration directory")
	}
	if _, err := io.Copy(io.Discard, gz); err != nil {
		t.Fatal(err)
	}
	return compressed
}

func startupMigrationApply(runPath string, calls *[]bool) StartupConfig {
	return StartupConfig{Apply: func(ini settings.StartupINI, values map[string]string, archived bool) error {
		*calls = append(*calls, archived)
		if slim, changed := settings.SlimStartupConfig(ini.Content, values, ini.RunMode); changed {
			return os.WriteFile(filepath.Join(runPath, "conf", "nps.conf"), []byte(slim), 0o600)
		}
		return nil
	}}
}

func verifyStartupMigrationLiveFiles(t *testing.T, runPath string, original map[string]string, migrated bool) {
	t.Helper()
	for name, expected := range original {
		path := filepath.Join(runPath, "conf", name)
		if migrated {
			switch name {
			case "clients.json", "users.json", "tasks.json", "hosts.json", "global.json",
				"clients.json.pre-gateway.bak.1.1.49", "nps.conf.pre-settings.bak.1.1.50.20261121-010203":
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("migration artifact %q remains: %v", name, err)
				}
				continue
			case "nps.conf":
				expected = startupMigrationBootConfig
			}
		}
		if actual := startupMigrationRead(t, path); string(actual) != expected {
			t.Fatalf("unexpected contents of %q", name)
		}
	}
	if _, err := os.Lstat(JSONGatewayMarkerPath(runPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unmarked migration created a live gateway marker: %v", err)
	}
}

func TestStartupMigrationArchivesOriginalAndPreservesLimits(t *testing.T) {
	runPath, original := startupMigrationFixture(t)
	var calls []bool
	config := startupMigrationApply(runPath, &calls)
	if err := initializeStorageWithConfig(runPath, config); err != nil {
		t.Fatal(err)
	}
	verifyStartupMigrationLiveFiles(t, runPath, original, true)
	report := startupMigrationReport(t, runPath)
	if report.Status != "success" || report.Counts != (migrationReportCounts{Clients: 2, Users: 1, Tunnels: 1, Hosts: 1, Settings: 3}) {
		t.Fatalf("unexpected import report: %#v", report)
	}
	backup := verifyStartupMigrationBackup(t, runPath, report, original)

	for _, id := range []int{12, 13} {
		client, err := Db.GetClient(id)
		if err != nil || client.UserId != 731 || client.RateLimit != 81920 {
			t.Fatalf("client %d lost ownership or did not convert exactly once: %v", id, err)
		}
	}
	owner, err := Db.GetUser(731)
	if err != nil || owner.MaxConn != nil || owner.BandwidthLimit == nil || *owner.BandwidthLimit != 40960 {
		t.Fatalf("user quotas did not migrate: %v", err)
	}
	tunnel, err := Db.GetTask(14)
	if err != nil || tunnel.Client.Id != 12 || tunnel.UploadLimit != 57344 || tunnel.DownloadLimit != 65536 {
		t.Fatalf("tunnel ownership or directional limits did not migrate: %v", err)
	}
	host, err := Db.GetHostById(15)
	if err != nil || host.Client.Id != 13 || Db.GetGlobal().RateLimit != 163840 {
		t.Fatalf("host ownership or global limit did not migrate: %v", err)
	}
	wantSettings := map[string]string{"allow_user_login": "false", "smtp_from": "sender@example.test", "register_max_conn": ""}
	if actual, err := Db.JsonDb.SQLite().LoadSettings(); err != nil || !maps.Equal(actual, wantSettings) {
		t.Fatalf("settings not preserved in SQLite: %v, %v", actual, err)
	}

	stopSnapshotRates(Db)
	if err := Db.JsonDb.SQLite().Close(); err != nil {
		t.Fatal(err)
	}
	residual := `{"Id":99,"VerifyKey":"old-copy"}`
	writeStartupMigrationFixture(t, runPath, "clients.json", residual)
	if err := initializeStorageWithConfig(runPath, config); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []bool{true, false}) || syncMapSize(&Db.JsonDb.Clients) != 2 {
		t.Fatalf("restart imported old JSON again: apply calls=%v", calls)
	}
	if actual := startupMigrationRead(t, filepath.Join(runPath, "conf", "clients.json")); string(actual) != residual {
		t.Fatal("restart touched a residual file outside its migration")
	}
	if !bytes.Equal(backup, verifyStartupMigrationBackup(t, runPath, startupMigrationReport(t, runPath), original)) {
		t.Fatal("a normal restart rewrote the migration backup")
	}
}

func TestStartupMigrationResumesAfterConfigApplyFailure(t *testing.T) {
	runPath, original := startupMigrationFixture(t)
	applyErr := errors.New("simulated configuration write failure")
	config := StartupConfig{Apply: func(_ settings.StartupINI, _ map[string]string, archived bool) error {
		if !archived {
			t.Error("migration backup was not available to Apply")
		}
		return applyErr
	}}
	if err := initializeStorageWithConfig(runPath, config); !errors.Is(err, applyErr) {
		t.Fatalf("expected simulated apply failure, got %v", err)
	}
	verifyStartupMigrationLiveFiles(t, runPath, original, false)
	failed := startupMigrationReport(t, runPath)
	if failed.Status != "failed" {
		t.Fatalf("configuration failure was reported as %q", failed.Status)
	}
	backup := verifyStartupMigrationBackup(t, runPath, failed, original)
	databasePath := filepath.Join(runPath, "conf", "nps.db")
	if _, err := os.Stat(databasePath); err != nil {
		t.Fatalf("committed SQLite is missing: %v", err)
	}
	database, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	values, readErr := database.LoadSettings()
	if err := database.Close(); err != nil || readErr != nil || values["smtp_from"] != "sender@example.test" {
		t.Fatalf("published data did not include the saved settings: %v, %v", readErr, err)
	}

	var calls []bool
	config = startupMigrationApply(runPath, &calls)
	tampered := slices.Clone(backup)
	tampered[4] ^= 1
	if err := os.WriteFile(failed.BackupArchive, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := initializeStorageWithConfig(runPath, config); err == nil || !strings.Contains(err.Error(), "迁移备份内容已变化") || len(calls) != 0 {
		t.Fatalf("tampered archive did not block cleanup: error=%v, apply calls=%v", err, calls)
	}
	verifyStartupMigrationLiveFiles(t, runPath, original, false)
	if err := os.WriteFile(failed.BackupArchive, backup, 0o600); err != nil {
		t.Fatal(err)
	}
	writeStartupMigrationFixture(t, runPath, "hosts.json.bak.1.1.48", "added after backup")
	if err := initializeStorageWithConfig(runPath, config); err != nil {
		t.Fatal(err)
	}
	verifyStartupMigrationLiveFiles(t, runPath, original, true)
	if actual := startupMigrationRead(t, filepath.Join(runPath, "conf", "hosts.json.bak.1.1.48")); string(actual) != "added after backup" {
		t.Fatal("cleanup did not preserve an unarchived file")
	}
	retried := startupMigrationReport(t, runPath)
	if retried.Status != "success" || retried.BackupArchive != failed.BackupArchive || !slices.Equal(calls, []bool{true}) {
		t.Fatalf("retry did not finish the original migration: backup=%q, status=%q, apply calls=%v", retried.BackupArchive, retried.Status, calls)
	}
	if !bytes.Equal(backup, verifyStartupMigrationBackup(t, runPath, retried, original)) {
		t.Fatal("retry changed the original backup")
	}
}

func TestStartupMigrationMalformedJSONKeepsOriginalFiles(t *testing.T) {
	runPath, original := startupMigrationFixture(t)
	original["tasks.json"] = `{"Id":`
	writeStartupMigrationFixture(t, runPath, "tasks.json", original["tasks.json"])
	var calls []bool
	if err := initializeStorageWithConfig(runPath, startupMigrationApply(runPath, &calls)); err == nil || len(calls) != 0 {
		t.Fatalf("malformed input reached Apply: error=%v, apply calls=%v", err, calls)
	}
	verifyStartupMigrationLiveFiles(t, runPath, original, false)
	if _, err := os.Lstat(filepath.Join(runPath, "conf", "nps.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid import published a live database: %v", err)
	}
	report := startupMigrationReport(t, runPath)
	if report.Status != "failed" {
		t.Fatalf("malformed input was reported as %q", report.Status)
	}
	verifyStartupMigrationBackup(t, runPath, report, original)
}

func TestStartupMigrationInvalidSettingsDoesNotPublishDatabase(t *testing.T) {
	runPath, original := startupMigrationFixture(t)
	original["nps.conf"] += "invalid setting line\n"
	writeStartupMigrationFixture(t, runPath, "nps.conf", original["nps.conf"])
	var calls []bool
	if err := initializeStorageWithConfig(runPath, startupMigrationApply(runPath, &calls)); err == nil || len(calls) != 0 {
		t.Fatalf("invalid settings reached Apply: error=%v, apply calls=%v", err, calls)
	}
	verifyStartupMigrationLiveFiles(t, runPath, original, false)
	if _, err := os.Stat(filepath.Join(runPath, "conf", "nps.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed settings import published a live database: %v", err)
	}
	report := startupMigrationReport(t, runPath)
	if report.Status != "failed" {
		t.Fatalf("invalid settings were reported as %q", report.Status)
	}
	verifyStartupMigrationBackup(t, runPath, report, original)
}

func TestStartupMigrationBackupFailureKeepsOriginalFiles(t *testing.T) {
	runPath, original := startupMigrationFixture(t)
	writeStartupMigrationFixture(t, runPath, "backups", "not a directory")
	var calls []bool
	if err := initializeStorageWithConfig(runPath, startupMigrationApply(runPath, &calls)); err == nil || !strings.Contains(err.Error(), "备份") || len(calls) != 0 {
		t.Fatalf("backup failure reached Apply: error=%v, apply calls=%v", err, calls)
	}
	verifyStartupMigrationLiveFiles(t, runPath, original, false)
	if _, err := os.Stat(filepath.Join(runPath, "conf", "nps.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup failure published a live database: %v", err)
	}
}

func TestStartupRejectsEmptyDatabaseLeftByLegacyMigration(t *testing.T) {
	runPath, original := startupMigrationFixture(t)
	confDir := filepath.Join(runPath, "conf")
	databasePath := filepath.Join(confDir, "nps.db")
	database, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	var calls []bool
	if err := initializeStorageWithConfig(runPath, startupMigrationApply(runPath, &calls)); err == nil ||
		!strings.Contains(err.Error(), "检测到空 nps.db") || len(calls) != 0 {
		t.Fatalf("interrupted legacy import reached the panel: err=%v apply=%v", err, calls)
	}
	verifyStartupMigrationLiveFiles(t, runPath, original, false)
	if _, err := os.Stat(filepath.Join(confDir, "backups")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup touched the interrupted migration: %v", err)
	}
	database, err = store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	meta, err := database.LoadMeta()
	if err != nil || meta["migration_completed"] != "" || settings.StartupConfigImported(meta) {
		t.Fatalf("startup incorrectly finalized the empty database: %v, %v", meta, err)
	}
}

func TestStartupMigrationDoesNotRestoreSettingDeletedFromIncludedConfig(t *testing.T) {
	runPath, original := startupMigrationFixture(t)
	// Business values from includes are read on the first import, but only
	// the main file is rewritten. Clearing an included value from the panel
	// must survive restart even though the include still contains it.
	original["runtime.conf"] = "totp_secret=synthetic-fixture\n"
	original["nps.conf"] += "include \"runtime.conf\"\n"
	writeStartupMigrationFixture(t, runPath, "nps.conf", original["nps.conf"])
	writeStartupMigrationFixture(t, runPath, "runtime.conf", original["runtime.conf"])
	var calls []bool
	config := startupMigrationApply(runPath, &calls)
	if err := initializeStorageWithConfig(runPath, config); err != nil {
		t.Fatal(err)
	}
	if got, exists := settings.Get("totp_secret"); !exists || got != "synthetic-fixture" {
		t.Fatal("first import did not read the included setting")
	}
	if err := settings.Save(nil, []string{"totp_secret"}); err != nil {
		t.Fatal(err)
	}
	stopSnapshotRates(Db)
	if err := Db.JsonDb.SQLite().Close(); err != nil {
		t.Fatal(err)
	}
	if err := initializeStorageWithConfig(runPath, config); err != nil {
		t.Fatal(err)
	}
	if _, exists := settings.Get("totp_secret"); exists || beego.AppConfig.String("totp_secret") != "" {
		t.Fatal("restart restored a deleted business setting from a legacy include")
	}
	if got := startupMigrationRead(t, filepath.Join(runPath, "conf", "runtime.conf")); string(got) != original["runtime.conf"] {
		t.Fatal("startup rewrote the included file")
	}
	if got := startupMigrationRead(t, filepath.Join(runPath, "conf", "nps.conf")); !strings.Contains(string(got), "include \"runtime.conf\"") {
		t.Fatal("startup removed the include directive")
	}
	verifyStartupMigrationBackup(t, runPath, startupMigrationReport(t, runPath), original)
}

func TestStartupAdoptsOldSQLiteWithoutImportingIncludeOnlySettings(t *testing.T) {
	runPath, original := startupMigrationFixture(t)
	confDir := filepath.Join(runPath, "conf")
	writeStartupMigrationFixture(t, runPath, "runtime.conf", "totp_secret=not-in-use\n")
	writeStartupMigrationFixture(t, runPath, "nps.conf", original["nps.conf"]+"include \"runtime.conf\"\n")
	database, err := store.Open(filepath.Join(confDir, "nps.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertSettings(map[string]string{"allow_user_login": "true"}); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	var calls []bool
	if err := initializeStorageWithConfig(runPath, startupMigrationApply(runPath, &calls)); err != nil {
		t.Fatal(err)
	}
	if got := settings.Snapshot(); !maps.Equal(got, map[string]string{"allow_user_login": "true", "smtp_from": "sender@example.test", "register_max_conn": ""}) {
		t.Fatalf("incorrect adoption of old database settings: %v", got)
	}
	if !slices.Equal(calls, []bool{false}) {
		t.Fatalf("old database incorrectly triggered JSON migration: %v", calls)
	}
}

func TestStartupUpgradesSQLiteFromEachPreRelease(t *testing.T) {
	for _, release := range []struct {
		version       string
		shippedHMAC   bool
		preloadedHMAC bool
		wantHMAC      map[string]string
	}{
		{
			version: "1.1.49",
			wantHMAC: map[string]string{
				"api_hmac_enabled":       "",
				"api_hmac_time_window":   "",
				"api_legacy_md5_enabled": "",
			},
		},
		{
			version: "1.1.50",
			wantHMAC: map[string]string{
				"api_hmac_enabled":       "",
				"api_hmac_time_window":   "",
				"api_legacy_md5_enabled": "",
			},
		},
		{
			version:     "1.1.51",
			shippedHMAC: true,
			wantHMAC: map[string]string{
				"api_hmac_enabled":       "true",
				"api_hmac_time_window":   "90",
				"api_legacy_md5_enabled": "false",
			},
		},
		{
			version:       "1.1.51",
			preloadedHMAC: true,
			wantHMAC: map[string]string{
				"api_hmac_enabled":       "false",
				"api_hmac_time_window":   "120",
				"api_legacy_md5_enabled": "true",
			},
		},
	} {
		name := release.version
		if release.shippedHMAC {
			name += "-shipped-hmac-config"
		} else if release.preloadedHMAC {
			name += "-preloaded-hmac-row"
		}
		t.Run(name, func(t *testing.T) {
			runPath := t.TempDir()
			trackStorageFixture(t)
			confDir := filepath.Join(runPath, "conf")
			if err := os.MkdirAll(confDir, 0o750); err != nil {
				t.Fatal(err)
			}
			businessConfig := `
allow_user_login=false
smtp_from="startup@example.test"
register_max_conn=0
`
			if release.shippedHMAC {
				businessConfig += `api_hmac_enabled=true
api_hmac_time_window=90
api_legacy_md5_enabled=false
`
			}
			writeStartupMigrationFixture(t, runPath, "nps.conf", startupMigrationBootConfig+businessConfig)
			databasePath := filepath.Join(confDir, "nps.db")
			seedPreReleaseSQLite(t, databasePath, release.version, release.preloadedHMAC)

			var calls []bool
			if err := initializeStorageWithConfig(runPath, startupMigrationApply(runPath, &calls)); err != nil {
				t.Fatal(err)
			}
			client, err := Db.GetClient(41)
			if err != nil || client.UserId != 77 {
				t.Fatalf("old SQLite client lost: %#v, %v", client, err)
			}
			user, err := Db.GetUser(77)
			if err != nil || user.UserName != "legacy-user" {
				t.Fatalf("old SQLite user lost: %#v, %v", user, err)
			}
			tunnel, err := Db.GetTask(51)
			if err != nil || tunnel.Client.Id != 41 {
				t.Fatalf("old SQLite tunnel lost: %#v, %v", tunnel, err)
			}
			host, err := Db.GetHostById(61)
			if err != nil || host.Client.Id != 41 {
				t.Fatalf("old SQLite host lost: %#v, %v", host, err)
			}
			values, err := Db.JsonDb.SQLite().LoadSettings()
			if err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{
				"allow_user_login": "true",
				"smtp_host":        "sqlite.example.test",
			} {
				if got := values[key]; got != want {
					t.Fatalf("%s = %q, want stored value %q", key, got, want)
				}
			}
			startupValues := map[string]string{
				"smtp_from":         "startup@example.test",
				"register_max_conn": "",
			}
			for key, want := range startupValues {
				if got := values[key]; got != want {
					t.Fatalf("%s = %q, want startup value %q", key, got, want)
				}
			}
			// 1.1.49/50 did not manage HMAC keys in SQLite. Their real
			// nps.conf was also absent those keys, so the runtime defaults must
			// stay effective without inventing migrated rows. 1.1.51 shipped
			// the keys in nps.conf but did not manage them, so they must migrate
			// from the file. A manually populated SQLite row must win.
			for key, want := range release.wantHMAC {
				got := values[key]
				if got != want {
					t.Fatalf("%s = %q, want %q", key, got, want)
				}
				if want == "" {
					if _, exists := values[key]; exists {
						t.Fatalf("%s should not be invented for %s", key, name)
					}
				}
			}
			switch {
			case release.shippedHMAC:
				if got := beego.AppConfig.DefaultBool("api_hmac_enabled", true); !got {
					t.Fatal("migrated HMAC enabled value was not activated")
				}
				if got := beego.AppConfig.DefaultInt("api_hmac_time_window", 60); got != 90 {
					t.Fatalf("activated HMAC window = %d, want 90", got)
				}
				if got := beego.AppConfig.DefaultBool("api_legacy_md5_enabled", false); got {
					t.Fatal("migrated legacy MD5 value was not activated")
				}
			case release.preloadedHMAC:
				if got := beego.AppConfig.DefaultBool("api_hmac_enabled", true); got {
					t.Fatal("preloaded HMAC disabled value was not activated")
				}
				if got := beego.AppConfig.DefaultInt("api_hmac_time_window", 60); got != 120 {
					t.Fatalf("preloaded HMAC window = %d, want 120", got)
				}
				if got := beego.AppConfig.DefaultBool("api_legacy_md5_enabled", false); !got {
					t.Fatal("preloaded legacy MD5 value was not activated")
				}
			default:
				if got := beego.AppConfig.DefaultBool("api_hmac_enabled", true); !got {
					t.Fatal("HMAC default true was lost")
				}
				if got := beego.AppConfig.DefaultInt("api_hmac_time_window", 60); got != 60 {
					t.Fatalf("HMAC default window = %d, want 60", got)
				}
				if got := beego.AppConfig.DefaultBool("api_legacy_md5_enabled", false); got {
					t.Fatal("HMAC legacy MD5 default false was lost")
				}
			}
			meta, err := Db.JsonDb.SQLite().LoadMeta()
			if err != nil || !settings.StartupConfigImported(meta) {
				t.Fatalf("settings import marker = %#v, %v", meta, err)
			}
			if !slices.Equal(calls, []bool{false}) {
				t.Fatalf("legacy SQLite incorrectly triggered JSON migration: %v", calls)
			}
			if _, err := os.Stat(filepath.Join(confDir, "backups")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("SQLite upgrade created a JSON migration archive: %v", err)
			}
			configAfter, err := os.ReadFile(filepath.Join(confDir, "nps.conf"))
			if err != nil {
				t.Fatal(err)
			}
			for key := range settings.ManagedKeySet() {
				if strings.Contains(string(configAfter), "\n"+key+"=") {
					t.Fatalf("business key %s remains in nps.conf after upgrade", key)
				}
			}

			stopSnapshotRates(Db)
			if err := Db.JsonDb.SQLite().Close(); err != nil {
				t.Fatal(err)
			}
			calls = nil
			SetDbForTest(nil)
			if err := initializeStorageWithConfig(runPath, startupMigrationApply(runPath, &calls)); err != nil {
				t.Fatal(err)
			}
			restarted, err := Db.JsonDb.SQLite().LoadSettings()
			if err != nil || !maps.Equal(restarted, values) {
				t.Fatalf("restart changed settings: %v, %v", restarted, err)
			}
			if !slices.Equal(calls, []bool{false}) {
				t.Fatalf("restart repeated migration apply: %v", calls)
			}
		})
	}
}

func seedPreReleaseSQLite(t *testing.T, path, release string, withHMAC bool) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	statements := []string{
		`CREATE TABLE entities (kind TEXT NOT NULL, id INTEGER NOT NULL, data BLOB NOT NULL, updated_at INTEGER NOT NULL, PRIMARY KEY (kind, id))`,
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, type TEXT NOT NULL, updated_at INTEGER NOT NULL, updated_by TEXT)`,
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO entities(kind, id, data, updated_at) VALUES
			('client', 41, '{"Id":41,"VerifyKey":"legacy-vkey","Status":true,"UserId":77,"RateLimit":81920,"_NPSGatewayVersion":"` + release + `"}', 1),
			('user', 77, '{"Id":77,"UserName":"legacy-user","Status":true}', 1),
			('tunnel', 51, '{"Id":51,"Mode":"tcp","Port":9001,"Status":true,"Client":{"Id":41},"Target":{"TargetStr":"127.0.0.1:80"}}', 1),
			('host', 61, '{"Id":61,"Host":"legacy.example.test","Location":"/","Scheme":"http","Client":{"Id":41},"Target":{"TargetStr":"127.0.0.1:80"}}', 1)`,
		`INSERT INTO settings(key, value, type, updated_at) VALUES
			('allow_user_login', 'true', 'bool', 1),
			('smtp_host', 'sqlite.example.test', 'string', 1)`,
		`INSERT INTO meta(key, value) VALUES
			('schema_version', '1'),
			('migrated_from', 'json-gateway'),
			('migration_completed', '1'),
			('nps_version', '` + release + `')`,
	}
	if withHMAC {
		statements = append(statements, `INSERT INTO settings(key, value, type, updated_at) VALUES
			('api_hmac_enabled', 'false', 'bool', 1),
			('api_hmac_time_window', '120', 'int', 1),
			('api_legacy_md5_enabled', 'true', 'bool', 1)`)
	}
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestStartupMigrationRepairsLegacyClientOwnerWithoutUsersFile(t *testing.T) {
	runPath := t.TempDir()
	trackStorageFixture(t)
	writeGatewayTestRecords(t, filepath.Join(runPath, "conf", "clients.json"),
		`{"Id":1,"VerifyKey":"legacy-owner","WebUserName":"legacy-owner","WebPassword":"fixture","Status":true,"UserId":662}`)
	t.Cleanup(func() { CleanupUserRuntime(662) })
	if err := initializeStorage(runPath); err != nil {
		t.Fatal(err)
	}
	client, err := Db.GetClient(1)
	if err != nil || client.UserId != 662 {
		t.Fatalf("legacy client was not attached to its owner: %v", err)
	}
	user, err := Db.GetUser(662)
	if err != nil || user.UserName != "legacy-owner" {
		t.Fatalf("legacy owner was not recreated: %v", err)
	}
}
