package file

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/acl"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/rate"
	"ehang.io/nps/lib/settings"
	"ehang.io/nps/lib/store"
	"ehang.io/nps/lib/version"
	"github.com/astaxie/beego/logs"
)

var (
	storageOnce sync.Once
	storageErr  error
	Db          *DbUtils
	dbMu        sync.RWMutex
)

// InitStorage initializes the authoritative data store exactly once. Callers
// that can report a startup error should invoke it before any controller or
// proxy begins serving requests.
func InitStorage(runPath string) error {
	storageOnce.Do(func() {
		storageErr = initializeStorage(runPath)
	})
	return storageErr
}

func SetDbForTest(db *DbUtils) {
	dbMu.Lock()
	Db = db
	dbMu.Unlock()
}

func initializeStorage(runPath string) error {
	confPath := filepath.Join(runPath, "conf")
	if err := os.MkdirAll(confPath, 0o750); err != nil {
		return fmt.Errorf("创建配置目录: %w", err)
	}
	databasePath := filepath.Join(confPath, "nps.db")
	databaseExists := common.FileExists(databasePath)
	if !databaseExists && JSONDataExists(runPath) {
		// Validate the gateway before opening SQLite. A rejected legacy import
		// must not leave an empty database or lock file as a half-migrated install.
		if err := EnsureGatewayForSQLite(runPath); err != nil {
			return err
		}
	}
	sqlite, createdByThisOpen, err := store.OpenWithStatus(databasePath)
	if err != nil {
		return err
	}
	importCommitted := false
	// Once the exclusive process lock is held, OpenWithStatus is authoritative:
	// an existing file can only be considered present after that acquisition.
	databaseExists = !createdByThisOpen
	fail := func(err error) error {
		if !importCommitted && createdByThisOpen {
			_ = sqlite.DiscardIncompleteFiles()
		} else {
			_ = sqlite.Close()
		}
		return err
	}
	if databaseExists {
		hasEntities, err := sqlite.HasEntities()
		if err != nil {
			return fail(fmt.Errorf("检查 SQLite 数据: %w", err))
		}
		if !hasEntities {
			if err := sqlite.ValidateMigrated(); err != nil {
				return fail(err)
			}
		}
		if hasEntities {
			logResidualJSONAfterMigration(runPath)
			jsonDb := NewJsonDb(runPath)
			jsonDb.SetSQLite(sqlite)
			db := &DbUtils{JsonDb: jsonDb}
			if err := loadStorageFromSQLite(db, sqlite); err != nil {
				return fail(err)
			}
			setActiveDb(db)
			return nil
		}
	}
	if !JSONDataExists(runPath) {
		jsonDb := NewJsonDb(runPath)
		jsonDb.SetSQLite(sqlite)
		db := &DbUtils{JsonDb: jsonDb}
		if err := loadStorageFromSQLite(db, sqlite); err != nil {
			return fail(fmt.Errorf("加载空 SQLite 元数据: %w", err))
		}
		setActiveDb(db)
		return nil
	}
	if err := EnsureGatewayForSQLite(runPath); err != nil {
		return fail(err)
	}
	backupDir, err := backupConfDirectory(runPath)
	if err != nil {
		return fail(fmt.Errorf("备份 conf 目录: %w", err))
	}
	db, err := loadJSONImportSnapshot(runPath)
	if err != nil {
		_ = writeMigrationReport(runPath, sqlite, nil, backupDir, fmt.Errorf("JSON 网关用户归属修复失败: %w", err))
		return fail(fmt.Errorf("JSON 网关用户归属修复失败: %w", err))
	}
	jsonDb := db.JsonDb
	if err := validateJSONImport(db); err != nil {
		_ = writeMigrationReport(runPath, sqlite, db, backupDir, fmt.Errorf("JSON 数据校验失败: %w", err))
		return fail(fmt.Errorf("JSON 数据校验失败: %w", err))
	}
	if err := importSQLiteFromMemory(db, sqlite); err != nil {
		_ = writeMigrationReport(runPath, sqlite, db, backupDir, err)
		return fail(err)
	}
	importCommitted = true
	jsonDb.SetSQLite(sqlite)
	if err := archiveJSONDataFiles(runPath); err != nil {
		_ = writeMigrationReport(runPath, sqlite, db, backupDir, fmt.Errorf("SQLite 导入成功，但归档 JSON 数据失败: %w", err))
		// The database is complete at this point. Keep it so an operator can
		// start from SQLite after fixing the archive problem; deleting it while
		// some JSON files may already be renamed would leave no authoritative
		// copy of the data.
		_ = sqlite.Close()
		return fmt.Errorf("SQLite 导入成功，但归档 JSON 数据失败: %w；已保留 nps.db，请检查 conf 目录后重试启动", err)
	}
	if err := archiveJSONGatewayMarker(runPath); err != nil {
		_ = writeMigrationReport(runPath, sqlite, db, backupDir, fmt.Errorf("SQLite 导入成功，但归档 JSON 网关标记失败: %w", err))
		_ = sqlite.Close()
		return fmt.Errorf("SQLite 导入成功，但归档 JSON 网关标记失败: %w；已保留 nps.db，请检查 conf 目录后重试启动", err)
	}
	if err := writeMigrationReport(runPath, sqlite, db, backupDir, nil); err != nil {
		// The database is already consistent and the source files are archived.
		// A report failure must not turn a successful import into a refused
		// startup, but the operator still needs the diagnostic in the log.
		logs.Warn("write SQLite migration report failed: %v", err)
	}
	setActiveDb(db)
	return nil
}

func setActiveDb(db *DbUtils) {
	dbMu.Lock()
	Db = db
	dbMu.Unlock()
}

func ImportSQLiteFromJSON(runPath string) error {
	if !JSONDataExists(runPath) {
		return errors.New("未找到可导入的 JSON 数据")
	}
	if common.FileExists(filepath.Join(runPath, "conf", "nps.db")) {
		return errors.New("SQLite 数据库已存在，首次导入必须由启动流程完成")
	}
	if err := EnsureGatewayForSQLite(runPath); err != nil {
		return err
	}
	backupDir, err := backupConfDirectory(runPath)
	if err != nil {
		return fmt.Errorf("备份 conf 目录: %w", err)
	}
	sqlite, createdByThisOpen, err := store.OpenWithStatus(filepath.Join(runPath, "conf", "nps.db"))
	if err != nil {
		return err
	}
	if !createdByThisOpen {
		_ = sqlite.Close()
		return errors.New("SQLite 数据库已存在，首次导入必须由启动流程完成")
	}
	committed := false
	defer func() {
		if committed {
			_ = sqlite.Close()
		} else {
			_ = sqlite.DiscardIncompleteFiles()
		}
	}()
	if hasEntities, err := sqlite.HasEntities(); err != nil {
		return err
	} else if hasEntities {
		return errors.New("SQLite 数据库已有数据，拒绝覆盖导入")
	}
	db, err := loadJSONImportSnapshot(runPath)
	if err != nil {
		importErr := fmt.Errorf("JSON 网关用户归属修复失败: %w", err)
		_ = writeMigrationReport(runPath, nil, nil, backupDir, importErr)
		return importErr
	}
	if err := validateJSONImport(db); err != nil {
		importErr := fmt.Errorf("JSON 数据校验失败: %w", err)
		_ = writeMigrationReport(runPath, nil, db, backupDir, importErr)
		return importErr
	}
	if err := importSQLiteFromMemory(db, sqlite); err != nil {
		_ = writeMigrationReport(runPath, sqlite, db, backupDir, err)
		return err
	}
	// Once the SQLite transaction has committed, a later archive/report
	// failure must not delete the only authoritative copy of the data.
	committed = true
	if err := archiveJSONDataFiles(runPath); err != nil {
		_ = writeMigrationReport(runPath, sqlite, db, backupDir, err)
		return err
	}
	_ = archiveJSONGatewayMarker(runPath)
	if err := writeMigrationReport(runPath, sqlite, db, backupDir, nil); err != nil {
		logs.Warn("write SQLite migration report failed: %v", err)
	}
	return nil
}

// loadJSONImportSnapshot loads the JSON files without persistent side effects.
// The raw files remain the rollback source until SQLite commits, so ownership
// repair is calculated in memory and is not written back here.
func loadJSONImportSnapshot(runPath string) (*DbUtils, error) {
	jsonDb := NewJsonDb(runPath)
	jsonDb.LoadClientFromJsonFile()
	jsonDb.LoadUserFromJsonFile()
	jsonDb.LoadTaskFromJsonFile()
	jsonDb.LoadGlobalFromJsonFile()
	jsonDb.LoadHostFromJsonFile()
	jsonDb.ReconcilePlatformHostCertificates()
	db := &DbUtils{JsonDb: jsonDb}
	if err := db.migrateUsersFromClients(false); err != nil {
		return nil, err
	}
	return db, nil
}

type migrationReport struct {
	Schema          string                `json:"schema"`
	Status          string                `json:"status"`
	Version         string                `json:"nps_version"`
	MigratedAt      string                `json:"migrated_at"`
	GatewayVersion  string                `json:"gateway_version,omitempty"`
	BackupDirectory string                `json:"backup_directory,omitempty"`
	Counts          migrationReportCounts `json:"counts"`
	HostConflicts   int                   `json:"host_conflicts"`
	Warnings        []string              `json:"warnings,omitempty"`
	Error           string                `json:"error,omitempty"`
	Direction       string                `json:"direction,omitempty"`
}

type migrationReportCounts struct {
	Clients  int `json:"clients"`
	Users    int `json:"users"`
	Tunnels  int `json:"tunnels"`
	Hosts    int `json:"hosts"`
	Settings int `json:"settings,omitempty"`
}

// writeMigrationReport records what the import actually loaded. It is written
// to a stable path so an operator can inspect the result after an unattended
// upgrade. Sensitive values (passwords, VKeys, tokens) are never included.
func writeMigrationReport(runPath string, sqlite *store.Store, db *DbUtils, backupDir string, importErr error) error {
	report := migrationReport{
		Schema:          "nps-sqlite-migration-v1",
		Status:          "success",
		Version:         version.VERSION,
		MigratedAt:      time.Now().Format(time.RFC3339),
		BackupDirectory: backupDir,
	}
	if importErr != nil {
		report.Status = "failed"
		report.Error = importErr.Error()
	}
	if marker, err := ReadJSONGatewayMarker(runPath); err == nil {
		report.GatewayVersion = marker.Version
	} else if backupDir != "" {
		if backupMarker, backupErr := ReadJSONGatewayMarkerFromPath(filepath.Join(backupDir, jsonGatewayFile)); backupErr == nil {
			report.GatewayVersion = backupMarker.Version
		}
	}
	if db != nil && db.JsonDb != nil {
		report.Counts = migrationReportCounts{
			Clients: syncMapSize(&db.JsonDb.Clients),
			Users:   syncMapSize(&db.JsonDb.Users),
			Tunnels: syncMapSize(&db.JsonDb.Tasks),
			Hosts:   syncMapSize(&db.JsonDb.Hosts),
		}
		report.Warnings = migrationWarnings(db, report.Counts, backupDir)
	}
	if sqlite != nil {
		if entities, err := sqlite.LoadEntities(); err == nil {
			report.HostConflicts = countHostConflicts(entities[store.KindHost])
		}
		if values, err := sqlite.LoadSettings(); err == nil {
			report.Counts.Settings = len(values)
		}
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomically(filepath.Join(runPath, "conf", "migration-report.json"), data)
}

func migrationWarnings(db *DbUtils, counts migrationReportCounts, backupDir string) []string {
	warnings := make([]string, 0)
	if counts.Hosts == 0 && common.FileExists(db.JsonDb.HostFilePath) {
		// A source file with records but zero loaded hosts was already rejected
		// by validateJSONImport. This branch only documents a genuinely empty
		// dataset.
		warnings = append(warnings, "hosts.json 未包含可导入的域名规则")
	}
	if !jsonImportHadUsersFile(db.JsonDb.UserFilePath, backupDir) {
		warnings = append(warnings, "未找到 users.json；客户端归属由兼容迁移补全，无法恢复的客户端保持未分配")
	}
	db.JsonDb.Tasks.Range(func(_, value interface{}) bool {
		if tunnel, ok := value.(*Tunnel); ok && tunnel != nil {
			tunnel.RLock()
			flow := tunnel.Flow
			tunnel.RUnlock()
			if flow != nil && flow.AccountingVersion < directionalFlowAccountingVersion {
				warnings = append(warnings, "存在旧 Flow 计数；只用于展示，不应作为商业账单依据")
				return false
			}
		}
		return true
	})
	warnings = append(warnings, "MaxTunnelNum 继续只统计普通隧道；Host 域名规则由 MaxHostNum 独立统计")
	return warnings
}

// jsonImportHadUsersFile checks the pre-import location and its backup. The
// successful path archives users.json before writing the report, so looking
// only at the live path would produce a false "missing users.json" warning.
func jsonImportHadUsersFile(livePath, backupDir string) bool {
	if common.FileExists(livePath) {
		return true
	}
	if backupDir == "" {
		return false
	}
	return common.FileExists(filepath.Join(backupDir, filepath.Base(livePath)))
}

func countHostConflicts(rows map[int]json.RawMessage) int {
	loaded := make([]*Host, 0, len(rows))
	for _, id := range sortedEntityIDs(rows) {
		host := new(Host)
		if err := json.Unmarshal(rows[id], host); err != nil || host == nil || host.Client == nil {
			continue
		}
		host.Id = id
		loaded = append(loaded, host)
	}
	seen := make([]*Host, 0, len(loaded))
	conflicts := 0
	for _, host := range loaded {
		conflict := false
		for _, existing := range seen {
			if existing.Client != nil && host.Client != nil && existing.Client.Id == host.Client.Id {
				continue
			}
			if hostSchemesOverlap(host.Scheme, existing.Scheme) &&
				hostRulesOverlap(host.Host, existing.Host) &&
				hostLocationsOverlap(host.Location, existing.Location) {
				conflict = true
				break
			}
		}
		if conflict {
			conflicts++
			continue
		}
		seen = append(seen, host)
	}
	return conflicts
}

func importSQLiteFromMemory(db *DbUtils, sqlite *store.Store) error {
	entities := make(map[string]map[int]json.RawMessage)
	addSyncMap := func(kind string, m *sync.Map) {
		rows := make(map[int]json.RawMessage)
		m.Range(func(key, value interface{}) bool {
			id, ok := key.(int)
			if !ok || id < 0 {
				return true
			}
			data, keep := marshalStoredEntity(value)
			if keep {
				rows[id] = data
			}
			return true
		})
		entities[kind] = rows
	}
	addSyncMap(store.KindClient, &db.JsonDb.Clients)
	addSyncMap(store.KindUser, &db.JsonDb.Users)
	addSyncMap(store.KindTunnel, &db.JsonDb.Tasks)
	addSyncMap(store.KindHost, &db.JsonDb.Hosts)
	if global := db.JsonDb.getGlobal(); global != nil {
		data, err := json.Marshal(global)
		if err != nil {
			return err
		}
		entities[store.KindGlobal] = map[int]json.RawMessage{0: data}
	}
	meta := map[string]string{
		"schema_version":      fmt.Sprint(store.SchemaVersion),
		"migrated_from":       "json-gateway",
		"migration_completed": "1",
		"nps_version":         version.VERSION,
		"migrated_at":         time.Now().Format(time.RFC3339),
	}
	for key, value := range db.JsonDb.idHighWaterMeta() {
		meta[key] = value
	}
	if marker, err := ReadJSONGatewayMarker(db.JsonDb.RunPath); err == nil {
		meta["json_gateway_version"] = marker.Version
	}
	if err := sqlite.ReplaceEntitiesWithMeta(entities, meta); err != nil {
		return fmt.Errorf("导入 SQLite: %w", err)
	}
	return nil
}

// validateJSONImport rejects a migration that would silently drop records or
// leave dangling ownership. The JSON loaders skip malformed entries by design
// for runtime resilience, so SQLite import must compare the source file count
// with the in-memory result instead of treating a smaller snapshot as success.
func validateJSONImport(db *DbUtils) error {
	if db == nil || db.JsonDb == nil {
		return errors.New("JSON 数据上下文无效")
	}
	sourceClients, err := countJSONRecords(db.JsonDb.ClientFilePath)
	if err != nil {
		return err
	}
	usersFileExists := common.FileExists(db.JsonDb.UserFilePath)
	sourceUsers, err := countJSONRecords(db.JsonDb.UserFilePath)
	if err != nil {
		return err
	}
	sourceTasks, err := countJSONRecords(db.JsonDb.TaskFilePath)
	if err != nil {
		return err
	}
	sourceHosts, err := countJSONRecords(db.JsonDb.HostFilePath)
	if err != nil {
		return err
	}
	clients, users, tunnels, hosts := syncMapSize(&db.JsonDb.Clients), syncMapSize(&db.JsonDb.Users), syncMapSize(&db.JsonDb.Tasks), syncMapSize(&db.JsonDb.Hosts)
	if clients != sourceClients {
		return fmt.Errorf("客户端记录不一致：文件 %d 条，加载 %d 条", sourceClients, clients)
	}
	// Legacy layouts without users.json rely on MigrateUsersFromClients to
	// synthesize dashboard accounts from client credentials. The in-memory
	// count is therefore intentionally larger than the source file; only
	// enforce equality when users.json is actually present.
	if usersFileExists && users != sourceUsers {
		return fmt.Errorf("用户记录不一致：文件 %d 条，加载 %d 条", sourceUsers, users)
	}
	if err := validateLoadedIDs(db.JsonDb.ClientFilePath, &db.JsonDb.Clients); err != nil {
		return fmt.Errorf("客户端 ID 不一致: %w", err)
	}
	if err := validateLoadedIDs(db.JsonDb.TaskFilePath, &db.JsonDb.Tasks); err != nil {
		return fmt.Errorf("隧道 ID 不一致: %w", err)
	}
	if tunnels != sourceTasks {
		return fmt.Errorf("隧道记录不一致：文件 %d 条，加载 %d 条", sourceTasks, tunnels)
	}
	if hosts != sourceHosts {
		// The host loader intentionally drops overlapping routes during
		// startup. Importing that filtered snapshot would silently lose the
		// original record, so require the conflicts to be resolved first.
		return fmt.Errorf("域名规则记录不一致：文件 %d 条，加载 %d 条；存在被跳过的冲突或无效规则，请先修正 hosts.json", sourceHosts, hosts)
	}
	if err := validateLoadedIDs(db.JsonDb.HostFilePath, &db.JsonDb.Hosts); err != nil {
		return fmt.Errorf("域名规则 ID 不一致: %w", err)
	}
	if err := validateLoadedReferences(db); err != nil {
		return err
	}
	return nil
}

// validateLoadedIDs verifies that every persisted record ID survived loading.
// A duplicate or malformed record can leave the sync.Map with the same
// cardinality as the file while replacing an earlier record.
func validateLoadedIDs(path string, table *sync.Map) error {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	seen := make(map[int]struct{})
	for _, record := range strings.Split(string(content), "\n"+common.CONN_DATA_SEQ) {
		trimmed := strings.TrimSpace(record)
		if trimmed == "" || trimmed == "null" {
			continue
		}
		var header struct {
			Id int `json:"Id"`
		}
		if err := json.Unmarshal([]byte(trimmed), &header); err != nil {
			return fmt.Errorf("记录不是有效 JSON: %w", err)
		}
		if header.Id <= 0 {
			return fmt.Errorf("记录缺少有效 ID")
		}
		if _, duplicate := seen[header.Id]; duplicate {
			return fmt.Errorf("记录 ID %d 重复", header.Id)
		}
		seen[header.Id] = struct{}{}
		if _, exists := table.Load(header.Id); !exists {
			return fmt.Errorf("记录 %d 未被加载", header.Id)
		}
	}
	return nil
}

func countJSONRecords(path string) (int, error) {
	if strings.TrimSpace(path) == "" {
		return 0, nil
	}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, record := range strings.Split(string(content), "\n"+common.CONN_DATA_SEQ) {
		trimmed := strings.TrimSpace(record)
		if trimmed == "" || trimmed == "null" {
			continue
		}
		count++
	}
	return count, nil
}

func syncMapSize(table *sync.Map) int {
	if table == nil {
		return 0
	}
	count := 0
	table.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

func validateLoadedReferences(db *DbUtils) error {
	validClients := make(map[int]struct{})
	validUsers := make(map[int]struct{})
	db.JsonDb.Clients.Range(func(key, _ interface{}) bool {
		if id, ok := key.(int); ok && id > 0 {
			validClients[id] = struct{}{}
		}
		return true
	})
	db.JsonDb.Users.Range(func(key, _ interface{}) bool {
		if id, ok := key.(int); ok && id > 0 {
			validUsers[id] = struct{}{}
		}
		return true
	})
	var referenceErr error
	db.JsonDb.Clients.Range(func(_, value interface{}) bool {
		client, ok := value.(*Client)
		if !ok || client == nil {
			return true
		}
		client.RLock()
		userID, clientID := client.UserId, client.Id
		client.RUnlock()
		if userID > 0 {
			if _, exists := validUsers[userID]; !exists {
				referenceErr = fmt.Errorf("客户端 %d 引用了不存在的用户 %d", clientID, userID)
				return false
			}
		}
		return true
	})
	if referenceErr != nil {
		return referenceErr
	}
	db.JsonDb.Tasks.Range(func(_, value interface{}) bool {
		tunnel, ok := value.(*Tunnel)
		if !ok || tunnel == nil {
			return true
		}
		tunnel.RLock()
		client, tunnelID := tunnel.Client, tunnel.Id
		tunnel.RUnlock()
		if client == nil {
			referenceErr = fmt.Errorf("隧道 %d 未关联客户端", tunnelID)
			return false
		}
		client.RLock()
		clientID := client.Id
		client.RUnlock()
		if _, exists := validClients[clientID]; !exists {
			referenceErr = fmt.Errorf("隧道 %d 引用了不存在的客户端 %d", tunnelID, clientID)
			return false
		}
		return true
	})
	if referenceErr != nil {
		return referenceErr
	}
	db.JsonDb.Hosts.Range(func(_, value interface{}) bool {
		host, ok := value.(*Host)
		if !ok || host == nil {
			return true
		}
		host.RLock()
		client, hostID := host.Client, host.Id
		host.RUnlock()
		if client == nil {
			referenceErr = fmt.Errorf("域名规则 %d 未关联客户端", hostID)
			return false
		}
		client.RLock()
		clientID := client.Id
		client.RUnlock()
		if _, exists := validClients[clientID]; !exists {
			referenceErr = fmt.Errorf("域名规则 %d 引用了不存在的客户端 %d", hostID, clientID)
			return false
		}
		return true
	})
	return referenceErr
}

func loadStorageFromSQLite(db *DbUtils, sqlite *store.Store) error {
	entities, err := sqlite.LoadEntities()
	if err != nil {
		return fmt.Errorf("读取 SQLite: %w", err)
	}
	jsonDb := db.JsonDb
	if err := loadClientsFromEntities(jsonDb, entities[store.KindClient]); err != nil {
		return err
	}
	if err := loadUsersFromEntities(jsonDb, entities[store.KindUser]); err != nil {
		return err
	}
	if err := loadTunnelsFromEntities(jsonDb, entities[store.KindTunnel]); err != nil {
		return err
	}
	if err := loadGlobalsFromEntities(jsonDb, entities[store.KindGlobal]); err != nil {
		return err
	}
	if err := loadHostsFromEntities(jsonDb, entities[store.KindHost]); err != nil {
		return err
	}
	meta, err := sqlite.LoadMeta()
	if err != nil {
		return fmt.Errorf("读取 SQLite 元数据: %w", err)
	}
	if err := restorePersistentIDHighWater(jsonDb, meta); err != nil {
		return err
	}
	return nil
}

func restorePersistentIDHighWater(jsonDb *JsonDb, meta map[string]string) error {
	counters := []struct {
		key     string
		counter *int32
		name    string
	}{
		{metaClientIDHighWater, &jsonDb.ClientIncreaseId, "客户端"},
		{metaUserIDHighWater, &jsonDb.UserIncreaseId, "用户"},
		{metaTaskIDHighWater, &jsonDb.TaskIncreaseId, "隧道"},
		{metaHostIDHighWater, &jsonDb.HostIncreaseId, "域名规则"},
	}
	for _, item := range counters {
		raw, exists := meta[item.key]
		if !exists {
			continue
		}
		value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 32)
		if err != nil || value < 0 {
			return fmt.Errorf("SQLite %s ID 高水位 %q 无效", item.name, raw)
		}
		for current := atomic.LoadInt32(item.counter); int32(value) > current; current = atomic.LoadInt32(item.counter) {
			if atomic.CompareAndSwapInt32(item.counter, current, int32(value)) {
				break
			}
		}
	}
	return nil
}

func loadClientsFromEntities(jsonDb *JsonDb, rows map[int]json.RawMessage) error {
	ids := sortedEntityIDs(rows)
	for _, id := range ids {
		if id <= 0 {
			return fmt.Errorf("SQLite 客户端 ID %d 无效", id)
		}
		client := new(Client)
		if err := json.Unmarshal(rows[id], client); err != nil || client == nil {
			return fmt.Errorf("SQLite 客户端 %d 数据无效: %w", id, err)
		}
		if client.Id != 0 && client.Id != id {
			return fmt.Errorf("SQLite 客户端键 %d 与记录 ID %d 不一致", id, client.Id)
		}
		if client.NoStore {
			return fmt.Errorf("SQLite 客户端 %d 是运行期临时记录，权威存储中不允许 NoStore", id)
		}
		client.Id = id
		if client.RateLimit > 0 {
			client.Rate = rate.NewRate(rate.BytesPerSecondFromBps(client.RateLimit))
		} else {
			client.Rate = rate.NewRate(clientRateBytes(0))
		}
		client.Rate.Start()
		if client.Flow == nil {
			client.Flow = NewFlow()
		}
		client.Flow.NormalizeLegacyAccounting()
		if client.ConnectionHistory == nil {
			client.ConnectionHistory = &ConnectionHistory{}
		}
		client.syncSourceListsFromFields()
		client.CompileSourceLists(acl.ScopeUser, "client")
		client.NowConn = 0
		jsonDb.Clients.Store(id, client)
		if id > int(jsonDb.ClientIncreaseId) {
			jsonDb.ClientIncreaseId = int32(id)
		}
	}
	return nil
}

func loadUsersFromEntities(jsonDb *JsonDb, rows map[int]json.RawMessage) error {
	for _, id := range sortedEntityIDs(rows) {
		if id <= 0 {
			return fmt.Errorf("SQLite 用户 ID %d 无效", id)
		}
		user := new(User)
		if err := json.Unmarshal(rows[id], user); err != nil || user == nil {
			return fmt.Errorf("SQLite 用户 %d 数据无效: %w", id, err)
		}
		if user.Id != 0 && user.Id != id {
			return fmt.Errorf("SQLite 用户键 %d 与记录 ID %d 不一致", id, user.Id)
		}
		user.Id = id
		jsonDb.Users.Store(id, user)
		SetUserRuntimeLimits(id, user.MaxConn, user.BandwidthLimit)
		if id > int(jsonDb.UserIncreaseId) {
			jsonDb.UserIncreaseId = int32(id)
		}
	}
	return nil
}

func loadTunnelsFromEntities(jsonDb *JsonDb, rows map[int]json.RawMessage) error {
	for _, id := range sortedEntityIDs(rows) {
		if id <= 0 {
			return fmt.Errorf("SQLite 隧道 ID %d 无效", id)
		}
		tunnel := new(Tunnel)
		if err := json.Unmarshal(rows[id], tunnel); err != nil || tunnel == nil {
			return fmt.Errorf("SQLite 隧道 %d 数据无效: %w", id, err)
		}
		if tunnel.Id != 0 && tunnel.Id != id {
			return fmt.Errorf("SQLite 隧道键 %d 与记录 ID %d 不一致", id, tunnel.Id)
		}
		if tunnel.NoStore {
			return fmt.Errorf("SQLite 隧道 %d 是运行期临时记录，权威存储中不允许 NoStore", id)
		}
		if tunnel.Client == nil || tunnel.Target == nil {
			return fmt.Errorf("SQLite 隧道 %d 缺少客户端或目标", id)
		}
		client, err := jsonDb.GetClient(tunnel.Client.Id)
		if err != nil {
			return fmt.Errorf("SQLite 隧道 %d 引用了不存在的客户端 %d", id, tunnel.Client.Id)
		}
		tunnel.Id = id
		tunnel.Client = client
		tunnel.CompileSourceLists(acl.ScopeUser, "tunnel")
		tunnel.CompileDestinationACL()
		if tunnel.Mode == "mixProxy" && !tunnel.HttpProxy && !tunnel.Socks5Proxy {
			tunnel.HttpProxy = true
			tunnel.Socks5Proxy = true
		}
		if tunnel.Flow == nil {
			tunnel.Flow = NewFlow()
		}
		if tunnel.MaxConn < 0 {
			tunnel.MaxConn = 0
		}
		tunnel.Flow.NormalizeLegacyAccounting()
		tunnel.SetRateLimits(tunnel.UploadLimit, tunnel.DownloadLimit)
		tunnel.CurrentConnections = 0
		tunnel.RunError = ""
		jsonDb.Tasks.Store(id, tunnel)
		if id > int(jsonDb.TaskIncreaseId) {
			jsonDb.TaskIncreaseId = int32(id)
		}
	}
	return nil
}

func loadHostsFromEntities(jsonDb *JsonDb, rows map[int]json.RawMessage) error {
	loaded := make([]*Host, 0)
	for _, id := range sortedEntityIDs(rows) {
		if id <= 0 {
			return fmt.Errorf("SQLite 域名规则 ID %d 无效", id)
		}
		host := new(Host)
		if err := json.Unmarshal(rows[id], host); err != nil || host == nil {
			return fmt.Errorf("SQLite 域名规则 %d 数据无效: %w", id, err)
		}
		if host.Id != 0 && host.Id != id {
			return fmt.Errorf("SQLite 域名规则键 %d 与记录 ID %d 不一致", id, host.Id)
		}
		if host.NoStore {
			return fmt.Errorf("SQLite 域名规则 %d 是运行期临时记录，权威存储中不允许 NoStore", id)
		}
		if host.Client == nil || host.Target == nil {
			return fmt.Errorf("SQLite 域名规则 %d 缺少客户端或目标", id)
		}
		client, err := jsonDb.GetClient(host.Client.Id)
		if err != nil {
			return fmt.Errorf("SQLite 域名规则 %d 引用了不存在的客户端 %d", id, host.Client.Id)
		}
		host.Id = id
		host.Client = client
		host.CompileSourceLists(acl.ScopeUser, "host")
		if host.Flow == nil {
			host.Flow = NewFlow()
		}
		host.SetRateLimits(host.UploadLimit, host.DownloadLimit)
		host.Flow.NormalizeLegacyAccounting()
		loaded = append(loaded, host)
		if id > int(jsonDb.HostIncreaseId) {
			jsonDb.HostIncreaseId = int32(id)
		}
	}
	sort.SliceStable(loaded, func(i, j int) bool { return loaded[i].Id < loaded[j].Id })
	utils := &DbUtils{JsonDb: jsonDb}
	for _, host := range loaded {
		if utils.IsHostRouteConflict(host) {
			return fmt.Errorf("SQLite 域名规则 %d (%s%s) 与其他规则路由冲突", host.Id, host.Host, host.Location)
		}
		jsonDb.Hosts.Store(host.Id, host)
	}
	return nil
}

func loadGlobalsFromEntities(jsonDb *JsonDb, rows map[int]json.RawMessage) error {
	for id, raw := range rows {
		if id != 0 {
			return fmt.Errorf("SQLite 全局配置 ID %d 无效，只允许 ID 0", id)
		}
		if len(raw) == 0 {
			return errors.New("SQLite 全局配置为空")
		}
		if err := loadGlobalFromEntity(jsonDb, raw); err != nil {
			return err
		}
	}
	return nil
}

func loadGlobalFromEntity(jsonDb *JsonDb, raw json.RawMessage) error {
	global := new(Glob)
	if err := json.Unmarshal(raw, global); err != nil || global == nil {
		return fmt.Errorf("SQLite 全局配置无效: %w", err)
	}
	platformDomains, err := normalizePlatformDomains(global.PlatformDomains)
	if err != nil {
		return fmt.Errorf("SQLite 全局平台域名无效: %w", err)
	}
	global.PlatformDomains = platformDomains
	jsonDb.setGlobal(global)
	global.syncSourceListsFromFields()
	global.CompileSourceLists(acl.ScopeGlobal, "global")
	SetGlobalMaxConn(global.MaxConn)
	rate.SetGlobalLimit(rate.BytesPerSecondFromBps(global.RateLimit))
	return nil
}

func sortedEntityIDs(rows map[int]json.RawMessage) []int {
	ids := make([]int, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

func backupConfDirectory(runPath string) (string, error) {
	source := filepath.Join(runPath, "conf")
	target := filepath.Join(runPath, "conf.backup."+time.Now().Format("20060102-150405"))
	if err := copyDirectory(source, target); err != nil {
		return "", err
	}
	return target, nil
}

func copyDirectory(source, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		src := filepath.Join(source, entry.Name())
		dst := filepath.Join(target, entry.Name())
		if entry.IsDir() {
			if err := copyDirectory(src, dst); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(src, dst); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func archiveJSONDataFiles(runPath string) error {
	for _, path := range jsonDataFiles(runPath) {
		if !common.FileExists(path) {
			continue
		}
		target := path + ".bak." + version.VERSION
		if err := os.Rename(path, target); err != nil {
			return err
		}
	}
	return nil
}

func archiveJSONGatewayMarker(runPath string) error {
	path := JSONGatewayMarkerPath(runPath)
	if !common.FileExists(path) {
		return nil
	}
	return os.Rename(path, path+".bak")
}

// logResidualJSONAfterMigration explains a conf directory that still contains
// JSON snapshots next to an already-committed nps.db. This happens when the
// database import succeeded but the follow-up archive step failed and the
// process was restarted. The JSON files are intentionally left in place as a
// second copy; they are not authoritative and must not be deleted blindly.
func logResidualJSONAfterMigration(runPath string) {
	residual := residualJSONFiles(runPath)
	if len(residual) == 0 {
		return
	}
	logs.Warn("SQLite 已是权威数据源，但 conf 仍残留旧 JSON 文件（%s）；这些文件仅作备份，不会自动删除，请确认后手工归档或移除", strings.Join(residual, ", "))
}

func residualJSONFiles(runPath string) []string {
	residual := make([]string, 0)
	for _, path := range jsonDataFiles(runPath) {
		if common.FileExists(path) {
			residual = append(residual, filepath.Base(path))
		}
	}
	if common.FileExists(JSONGatewayMarkerPath(runPath)) {
		residual = append(residual, jsonGatewayFile)
	}
	return residual
}

// ExportSQLiteToJSON writes a rollback-compatible JSON snapshot. It converts
// bps/three-state fields back to the historical integer and KB/s layout used
// by the JSON version. The exported tree is marked as already normalized, so
// the old JSON release can start it directly without a second KB/s conversion.
func ExportSQLiteToJSON(runPath, outputDir string) error {
	if strings.TrimSpace(outputDir) == "" {
		return errors.New("JSON 导出目录不能为空")
	}
	if base := filepath.Base(filepath.Clean(outputDir)); base == "." || base == string(filepath.Separator) {
		return errors.New("JSON 导出目录必须指向新的专用目录")
	}
	parent := filepath.Dir(filepath.Clean(outputDir))
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return err
	}
	if info, err := os.Stat(outputDir); err == nil {
		if !info.IsDir() {
			return errors.New("JSON 导出目标不是目录")
		}
		entries, readErr := os.ReadDir(outputDir)
		if readErr != nil {
			return readErr
		}
		if len(entries) != 0 {
			return errors.New("JSON 导出目录必须为空，避免把不同时间的回滚快照混合在一起")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	staging, err := os.MkdirTemp(parent, "."+filepath.Base(outputDir)+".staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := exportSQLiteToJSONStaging(runPath, staging); err != nil {
		if reportData, reportErr := os.ReadFile(filepath.Join(staging, "migration-report.json")); reportErr == nil {
			if os.MkdirAll(outputDir, 0o750) == nil {
				if entries, readErr := os.ReadDir(outputDir); readErr == nil && len(entries) == 0 {
					_ = writeFileAtomically(filepath.Join(outputDir, "migration-report.json"), reportData)
				}
			}
		}
		return err
	}
	if err := os.Remove(outputDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("替换 JSON 导出目录: %w", err)
	}
	if err := os.Rename(staging, outputDir); err != nil {
		return fmt.Errorf("提交 JSON 导出目录: %w", err)
	}
	return nil
}

func exportSQLiteToJSONStaging(runPath, outputDir string) error {
	databasePath := filepath.Join(runPath, "conf", "nps.db")
	if !common.FileExists(databasePath) {
		return errors.New("SQLite 数据库不存在，无法导出")
	}
	sqlite, err := store.Open(databasePath)
	if err != nil {
		return err
	}
	defer sqlite.Close()
	if hasEntities, err := sqlite.HasEntities(); err != nil {
		return err
	} else if !hasEntities {
		return errors.New("SQLite 数据库没有可导出的实体")
	}
	entities, err := sqlite.LoadEntities()
	if err != nil {
		return err
	}
	settingsValues, err := sqlite.LoadSettings()
	if err != nil {
		return err
	}
	report := migrationReport{
		Schema:     "nps-sqlite-export-v1",
		Status:     "success",
		Direction:  "sqlite_to_json",
		Version:    version.VERSION,
		MigratedAt: time.Now().Format(time.RFC3339),
		Counts: migrationReportCounts{
			Clients:  len(entities[store.KindClient]),
			Users:    len(entities[store.KindUser]),
			Tunnels:  len(entities[store.KindTunnel]),
			Hosts:    len(entities[store.KindHost]),
			Settings: len(settingsValues),
		},
	}
	// Create the destination before validation so a rejected export still
	// leaves the machine-readable failure report operators rely on.
	if err := os.MkdirAll(outputDir, 0o750); err != nil {
		return err
	}
	if err := validateJSONExportQuotas(entities); err != nil {
		return finishJSONExportReport(outputDir, report, err)
	}
	confOut := filepath.Join(outputDir, "conf")
	if err := os.MkdirAll(confOut, 0o750); err != nil {
		return finishJSONExportReport(outputDir, report, err)
	}
	if err := exportNPSConfig(filepath.Join(runPath, "conf", "nps.conf"), filepath.Join(confOut, "nps.conf"), settingsValues); err != nil {
		return finishJSONExportReport(outputDir, report, err)
	}
	for kind, name := range map[string]string{
		store.KindClient: "clients.json",
		store.KindUser:   "users.json",
		store.KindTunnel: "tasks.json",
		store.KindHost:   "hosts.json",
	} {
		records := make([]string, 0, len(entities[kind]))
		for _, id := range sortedRawIDs(entities[kind]) {
			converted, err := exportEntityRecord(kind, entities[kind][id])
			if err != nil {
				return finishJSONExportReport(outputDir, report, fmt.Errorf("导出 %s/%d: %w", kind, id, err))
			}
			records = append(records, string(converted))
		}
		if err := writeFileAtomically(filepath.Join(confOut, name), []byte(strings.Join(records, "\n"+common.CONN_DATA_SEQ))); err != nil {
			return finishJSONExportReport(outputDir, report, err)
		}
	}
	if raw := entities[store.KindGlobal][0]; len(raw) > 0 {
		converted, err := exportEntityRecord(store.KindGlobal, raw)
		if err != nil {
			return finishJSONExportReport(outputDir, report, err)
		}
		if err := writeFileAtomically(filepath.Join(confOut, "global.json"), converted); err != nil {
			return finishJSONExportReport(outputDir, report, err)
		}
	}
	if err := writeJSONGatewayMarker(filepath.Join(confOut, jsonGatewayFile), existingJSONDataFileNames(outputDir)); err != nil {
		return finishJSONExportReport(outputDir, report, err)
	}
	if err := finishJSONExportReport(outputDir, report, nil); err != nil {
		return err
	}
	return nil
}

// exportNPSConfig creates a rollback-compatible configuration file. Startup
// parameters are copied from the current file, while every managed business
// setting is appended from SQLite because settings.Init removed those lines
// from nps.conf after the first SQLite startup.
func exportNPSConfig(sourcePath, outputPath string, values map[string]string) error {
	content, err := os.ReadFile(sourcePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("读取 nps.conf: %w", err)
	}
	lines := strings.SplitAfter(string(content), "\n")
	filtered := make([]string, 0, len(lines)+len(values)+3)
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r\n"))
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			filtered = append(filtered, line)
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			filtered = append(filtered, line)
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		if settings.IsManagedKey(key) || settings.IsLegacyRemovedKey(key) {
			continue
		}
		filtered = append(filtered, line)
	}
	normalizedValues := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.ToLower(strings.TrimSpace(key))
		if !settings.IsManagedKey(key) {
			return fmt.Errorf("SQLite 业务配置包含未知键 %q，拒绝生成不可回滚的 nps.conf", key)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("业务配置 %s 包含换行符，无法写入 nps.conf", key)
		}
		normalizedValues[key] = value
	}
	keys := make([]string, 0, len(normalizedValues))
	for key := range normalizedValues {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var builder strings.Builder
	builder.WriteString(strings.Join(filtered, ""))
	if builder.Len() > 0 && !strings.HasSuffix(builder.String(), "\n") {
		builder.WriteByte('\n')
	}
	builder.WriteString("\n###########################################################################\n")
	builder.WriteString("# 以下业务配置由 SQLite 导出，供 JSON 版本回滚启动\n")
	builder.WriteString("###########################################################################\n")
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(normalizedValues[key])
		builder.WriteByte('\n')
	}
	return writeFileAtomically(outputPath, []byte(builder.String()))
}

// finishJSONExportReport leaves a machine-readable diagnostic beside every
// export attempt. The data files are written before this report, so a failed
// export is distinguishable from an older successful snapshot.
func finishJSONExportReport(outputDir string, report migrationReport, exportErr error) error {
	if exportErr != nil {
		report.Status = "failed"
		report.Error = exportErr.Error()
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		if exportErr != nil {
			return exportErr
		}
		return err
	}
	if err := writeFileAtomically(filepath.Join(outputDir, "migration-report.json"), data); err != nil {
		if exportErr != nil {
			return fmt.Errorf("%v；写导出报告失败: %w", exportErr, err)
		}
		return fmt.Errorf("写导出报告失败: %w", err)
	}
	return exportErr
}

// validateJSONExportQuotas refuses values the historical JSON layout cannot
// represent without changing behavior. A zero user quota means "not included"
// in SQLite but "unlimited" in the old layout, so exporting it silently would
// turn a plan restriction into an unrestricted account.
func validateJSONExportQuotas(entities map[string]map[int]json.RawMessage) error {
	for id, raw := range entities[store.KindUser] {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return fmt.Errorf("导出用户 %d: %w", id, err)
		}
		for _, key := range []string{"MaxClientNum", "MaxTunnelNum", "MaxHostNum", "MaxConn", "BandwidthLimit"} {
			if value, ok := rawInt64(fields, key); ok && value == 0 {
				return fmt.Errorf("用户 %d 的 %s 为 0（套餐不包含），旧 JSON 格式只能表示“不限”；请先在管理面板改为留空或正整数后再导出", id, key)
			}
		}
	}
	return nil
}

func sortedRawIDs(rows map[int]json.RawMessage) []int {
	ids := make([]int, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

func exportEntityRecord(kind string, raw json.RawMessage) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	switch kind {
	case store.KindUser:
		exportUserQuantities(fields)
	case store.KindClient:
		if value, ok := rawInt64(fields, "RateLimit"); ok {
			fields["RateLimit"] = jsonNumber(rate.KBPerSecondFromBps(value))
		}
	case store.KindTunnel, store.KindHost:
		if value, ok := rawInt64(fields, "UploadLimit"); ok {
			fields["UploadLimit"] = jsonNumber(rate.KBPerSecondFromBps(value))
		}
		if value, ok := rawInt64(fields, "DownloadLimit"); ok {
			fields["DownloadLimit"] = jsonNumber(rate.KBPerSecondFromBps(value))
		}
	case store.KindGlobal:
		if value, ok := rawInt64(fields, "RateLimit"); ok {
			fields["RateLimit"] = jsonNumber(rate.KBPerSecondFromBps(value))
		}
	}
	return json.Marshal(fields)
}

func exportUserQuantities(fields map[string]json.RawMessage) {
	for _, key := range []string{"MaxClientNum", "MaxTunnelNum", "MaxHostNum", "MaxConn"} {
		if value, ok := rawInt64(fields, key); ok {
			fields[key] = jsonNumber(value)
		}
	}
	var bandwidth int64
	if value, ok := rawInt64(fields, "BandwidthLimit"); ok {
		bandwidth = rate.KBPerSecondFromBps(value)
	}
	delete(fields, "BandwidthLimit")
	if bandwidth > 0 {
		upload := bandwidth / 2
		download := bandwidth - upload
		fields["UploadLimit"] = jsonNumber(upload)
		fields["DownloadLimit"] = jsonNumber(download)
	} else {
		// A nil user budget is "unlimited" in the old layout, while an
		// explicit zero budget is "not included". Neither can be represented
		// as a positive directional pair.
		delete(fields, "UploadLimit")
		delete(fields, "DownloadLimit")
	}
}

func jsonNumber(value int64) json.RawMessage {
	return json.RawMessage(fmt.Sprintf("%d", value))
}

func newInt32(value int) int32 {
	if value < 0 {
		return 0
	}
	return int32(value)
}

func atomicMaxInt32(target *int32, value int) {
	for current := atomic.LoadInt32(target); value > int(current); current = atomic.LoadInt32(target) {
		if atomic.CompareAndSwapInt32(target, current, newInt32(value)) {
			return
		}
	}
}
