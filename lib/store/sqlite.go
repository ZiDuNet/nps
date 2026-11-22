package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/lib/durablefile"
)

const SchemaVersion = 1

// ErrSchemaTooNew prevents an older binary from opening a database that a
// newer release has already migrated. The generic entity format makes such a
// downgrade look harmless until the old process snapshots an entire entity
// kind and silently erases fields it does not understand.
var ErrSchemaTooNew = errors.New("数据库 schema 版本高于当前程序")

// ErrDatabaseLocked identifies the expected failure when another NPS process
// already owns the database. Startup code must never remove database files
// after this error because the other process is actively using them.
var ErrDatabaseLocked = errors.New("数据库已被另一个 NPS 进程占用")

// Entity kinds stored in the generic entity table. Keeping the existing
// structs as JSON values preserves nested health, ACL, target and platform
// metadata without creating a migration for every model change.
const (
	KindClient = "client"
	KindTunnel = "tunnel"
	KindHost   = "host"
	KindUser   = "user"
	KindGlobal = "global"
)

var ErrEmptyDatabase = errors.New("数据库为空")

type Store struct {
	db     *sql.DB
	lock   *os.File
	path   string
	mu     sync.Mutex
	write  sync.Mutex
	closed bool
}

func Open(path string) (*Store, error) {
	database, _, err := OpenWithStatus(path)
	return database, err
}

// ProbeLock reports whether another process currently owns the database
// lock. It only opens the reusable lock file and never creates a database,
// modifies data, or changes the lock file when one does not exist yet.
func ProbeLock(path string) (bool, error) {
	if path == "" {
		return false, errors.New("数据库路径不能为空")
	}
	lockPath := path + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("打开数据库锁文件: %w", err)
	}
	defer lock.Close()
	if err := lockDatabaseFile(lock); err != nil {
		if errors.Is(err, ErrDatabaseLocked) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// OpenWithStatus opens the database and reports whether this call created it
// after acquiring the process lock. Callers use created=false on every error
// path to avoid deleting a database that belongs to another process.
func OpenWithStatus(path string) (*Store, bool, error) {
	return openWithInitializer(path, nil)
}

// OpenInitialized builds a new store off to the side and only publishes it
// after initialization and verification have succeeded. The normal database
// lock covers both steps. A process interrupted before publication leaves no
// nps.db for the next startup to mistake for an initialized installation.
func OpenInitialized(path string, initialize func(dir string, database *Store) error) (*Store, error) {
	database, _, err := openWithInitializer(path, initialize)
	return database, err
}

func openWithInitializer(path string, initialize func(string, *Store) error) (*Store, bool, error) {
	if path == "" {
		return nil, false, errors.New("数据库路径不能为空")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, false, fmt.Errorf("创建数据库目录: %w", err)
	}
	lockPath := path + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("打开数据库锁文件: %w", err)
	}
	if err := lockDatabaseFile(lock); err != nil {
		_ = lock.Close()
		if errors.Is(err, ErrDatabaseLocked) {
			return nil, false, err
		}
		return nil, false, err
	}
	_, statErr := os.Lstat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		_ = lock.Close()
		return nil, false, fmt.Errorf("检查数据库文件: %w", statErr)
	}
	if created && initialize != nil {
		if err := initializeNewDatabase(path, initialize); err != nil {
			_ = lock.Close()
			return nil, false, err
		}
		// A completed, published database must be kept even if reopening it
		// fails. No later error may discard these committed data files.
		created = false
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String() +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		discardPartialOpen(path, nil, lock, created)
		return nil, created, fmt.Errorf("打开 SQLite: %w", err)
	}
	// modernc SQLite is safe for serialized callers; the runtime keeps one
	// connection so WAL and snapshot writes cannot surprise each other.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		discardPartialOpen(path, db, lock, created)
		return nil, created, fmt.Errorf("连接 SQLite: %w", err)
	}
	store := &Store{db: db, lock: lock, path: path}
	if err := os.Chmod(path, 0o600); err != nil {
		closeAfterOpenFailure(store, created)
		return nil, created, fmt.Errorf("设置数据库文件权限: %w", err)
	}
	if err := store.initialize(); err != nil {
		closeAfterOpenFailure(store, created)
		return nil, created, err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Chmod(path+suffix, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			closeAfterOpenFailure(store, created)
			return nil, created, fmt.Errorf("设置数据库附属文件权限: %w", err)
		}
	}
	return store, created, nil
}

func initializeNewDatabase(path string, initialize func(string, *Store) error) error {
	dir, err := os.MkdirTemp(filepath.Dir(path), ".sqlite-init-")
	if err != nil {
		return fmt.Errorf("创建 SQLite 初始化暂存目录: %w", err)
	}
	defer os.RemoveAll(dir) // exclusively owned staging directory
	stagedPath := filepath.Join(dir, filepath.Base(path))
	staged, err := Open(stagedPath)
	if err != nil {
		return err
	}
	defer staged.Close()
	if err := initialize(dir, staged); err != nil {
		return err
	}
	var result string
	if err := staged.db.QueryRow(`PRAGMA quick_check`).Scan(&result); err != nil || result != "ok" {
		return errors.New("SQLite 初始化完整性校验失败")
	}
	if err := staged.Sync(); err != nil {
		return fmt.Errorf("同步暂存数据库: %w", err)
	}
	if err := staged.Close(); err != nil {
		return fmt.Errorf("关闭暂存数据库: %w", err)
	}
	// Never attach WAL files that did not belong to the staged database.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("数据库文件缺失，但 %s 存在或无法检查；请从备份恢复", filepath.Base(path)+suffix)
		}
	}
	if err := durablefile.Rename(stagedPath, path); err != nil {
		return fmt.Errorf("发布初始化完成的 SQLite 数据库: %w", err)
	}
	return nil
}

// Sync checkpoints SQLite before the caller retires an older data source.
// The normal runtime uses WAL with synchronous=NORMAL to avoid an fsync for
// every snapshot; a one-time startup migration needs a stronger boundary.
func (s *Store) Sync() error {
	s.write.Lock()
	defer s.write.Unlock()
	var busy, logPages, checkpointed int
	if err := s.db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logPages, &checkpointed); err != nil {
		return fmt.Errorf("SQLite WAL 合并失败: %w", err)
	}
	if busy != 0 {
		return errors.New("SQLite WAL 正在占用，无法完成合并")
	}
	f, err := os.OpenFile(s.path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func closeAfterOpenFailure(database *Store, created bool) {
	if created {
		_ = database.DiscardIncompleteFiles()
		return
	}
	_ = database.Close()
}

func discardPartialOpen(path string, db *sql.DB, lock *os.File, created bool) {
	if db != nil {
		_ = db.Close()
	}
	if created {
		removeDatabaseDataFiles(path)
	}
	if lock != nil {
		_ = lock.Close()
	}
}

func (s *Store) initialize() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS entities (
			kind TEXT NOT NULL,
			id INTEGER NOT NULL,
			data BLOB NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (kind, id)
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			type TEXT NOT NULL,
			updated_at INTEGER NOT NULL,
			updated_by TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("初始化 SQLite: %w", err)
		}
	}
	var current string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&current)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.Exec(`INSERT INTO meta(key, value) VALUES ('schema_version', ?)`, fmt.Sprint(SchemaVersion)); err != nil {
			return fmt.Errorf("写入 schema 版本: %w", err)
		}
	case err != nil:
		return fmt.Errorf("读取 schema 版本: %w", err)
	default:
		var version int
		if _, err := fmt.Sscanf(current, "%d", &version); err != nil {
			return fmt.Errorf("schema 版本无效: %q", current)
		}
		if version > SchemaVersion {
			return fmt.Errorf("%w: 数据库=%d 程序=%d", ErrSchemaTooNew, version, SchemaVersion)
		}
		if version < SchemaVersion {
			return fmt.Errorf("数据库 schema 版本 %d 低于当前程序 %d，需要先运行对应的升级工具", version, SchemaVersion)
		}
	}
	return nil
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var closeErr error
	if s.db != nil {
		closeErr = s.db.Close()
	}
	if s.lock != nil {
		if err := s.lock.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

// DiscardIncompleteFiles removes a database created by a failed first import.
// It deliberately keeps the process lock held until the data files are gone,
// so another NPS process cannot acquire the lock and create a database that
// this process then deletes. The reusable .lock file is left in place.
func (s *Store) DiscardIncompleteFiles() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var closeErr error
	if s.db != nil {
		closeErr = s.db.Close()
	}
	var removeErr error
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(s.path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			removeErr = errors.Join(removeErr, err)
		}
	}
	if s.lock != nil {
		if err := s.lock.Close(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
	}
	return errors.Join(removeErr, closeErr)
}

func removeDatabaseDataFiles(path string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
}

func (s *Store) Empty() (bool, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM entities`).Scan(&count); err != nil {
		return false, err
	}
	return count == 0, nil
}

func (s *Store) HasEntities() (bool, error) {
	empty, err := s.Empty()
	return !empty, err
}

// ValidateMigrated distinguishes an initialized database from a truncated or
// interrupted import. An empty entities table with migration metadata is not a
// fresh installation; silently creating an empty system there can expose a
// server with missing access-control data.
func (s *Store) ValidateMigrated() error {
	if s == nil {
		return errors.New("SQLite store 未初始化")
	}
	var migrated string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'migrated_from'`).Scan(&migrated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var completed string
	err = s.db.QueryRow(`SELECT value FROM meta WHERE key = 'migration_completed'`).Scan(&completed)
	if err == nil && completed == "1" {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return errors.New("SQLite 数据库包含迁移元数据但实体表为空；拒绝按全新安装启动，请从 conf 备份恢复")
}

func (s *Store) SchemaVersion() (int, error) {
	var value string
	if err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&value); err != nil {
		return 0, err
	}
	var version int
	_, err := fmt.Sscanf(value, "%d", &version)
	return version, err
}

func (s *Store) ReplaceEntities(entities map[string]map[int]json.RawMessage) error {
	return s.replaceEntities(entities, nil)
}

// ReplaceEntitiesWithMeta replaces all entities and migration metadata in the
// same transaction. A crash can therefore never leave a database that has
// entities but no completed migration marker.
func (s *Store) ReplaceEntitiesWithMeta(entities map[string]map[int]json.RawMessage, meta map[string]string) error {
	return s.replaceEntities(entities, meta)
}

func (s *Store) replaceEntities(entities map[string]map[int]json.RawMessage, meta map[string]string) error {
	s.write.Lock()
	defer s.write.Unlock()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM entities`); err != nil {
		return err
	}
	now := time.Now().Unix()
	for kind, rows := range entities {
		for id, data := range rows {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO entities(kind, id, data, updated_at) VALUES (?, ?, ?, ?)`,
				kind, id, []byte(data), now,
			); err != nil {
				return fmt.Errorf("写入 %s/%d: %w", kind, id, err)
			}
		}
	}
	for key, value := range meta {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO meta(key, value) VALUES (?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			key, value,
		); err != nil {
			return fmt.Errorf("写入 meta %s: %w", key, err)
		}
	}
	return tx.Commit()
}

func (s *Store) UpsertEntity(kind string, id int, data []byte) error {
	s.write.Lock()
	defer s.write.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO entities(kind, id, data, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(kind, id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		kind, id, data, time.Now().Unix(),
	)
	return err
}

func (s *Store) UpsertEntities(kind string, rows map[int][]byte) error {
	s.write.Lock()
	defer s.write.Unlock()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	for id, data := range rows {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO entities(kind, id, data, updated_at) VALUES (?, ?, ?, ?)
			 ON CONFLICT(kind, id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
			kind, id, data, now,
		); err != nil {
			return fmt.Errorf("写入 %s/%d: %w", kind, id, err)
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteEntity(kind string, id int) error {
	s.write.Lock()
	defer s.write.Unlock()
	_, err := s.db.Exec(`DELETE FROM entities WHERE kind = ? AND id = ?`, kind, id)
	return err
}

func (s *Store) ReplaceKind(kind string, rows map[int][]byte) error {
	return s.ReplaceKinds(map[string]map[int][]byte{kind: rows})
}

// ReplaceKinds applies a multi-kind snapshot in one transaction. Each kind is
// diffed against the current rows, so unchanged entities keep their original
// updated_at value and are not rewritten by periodic flow persistence.
func (s *Store) ReplaceKinds(snapshots map[string]map[int][]byte) error {
	return s.ReplaceKindsWithMeta(snapshots, nil)
}

// ReplaceKindsWithMeta commits entity snapshots and durable counters in one
// transaction. ID high-water marks must advance together with newly stored
// rows so deleting the current maximum record cannot make that ID available
// again after a restart.
func (s *Store) ReplaceKindsWithMeta(snapshots map[string]map[int][]byte, meta map[string]string) error {
	if s == nil {
		return errors.New("SQLite store 未初始化")
	}
	// Serialize snapshot reconciliation. A delayed tunnel snapshot must not
	// recreate a tunnel deleted by a concurrent UI request, and multi-kind
	// operations must commit or roll back as one unit.
	s.write.Lock()
	defer s.write.Unlock()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	for kind, rows := range snapshots {
		if strings.TrimSpace(kind) == "" {
			return errors.New("实体类型不能为空")
		}
		existing, err := loadKindRows(ctx, tx, kind)
		if err != nil {
			return err
		}
		for id, data := range rows {
			if current, found := existing[id]; found && bytes.Equal(current, data) {
				delete(existing, id)
				continue
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO entities(kind, id, data, updated_at) VALUES (?, ?, ?, ?)
				 ON CONFLICT(kind, id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
				kind, id, data, now,
			); err != nil {
				return fmt.Errorf("写入 %s/%d: %w", kind, id, err)
			}
			delete(existing, id)
		}
		for id := range existing {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM entities WHERE kind = ? AND id = ?`,
				kind, id,
			); err != nil {
				return fmt.Errorf("删除 %s/%d: %w", kind, id, err)
			}
		}
	}
	for key, value := range meta {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO meta(key, value) VALUES (?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			key, value,
		); err != nil {
			return fmt.Errorf("写入 meta %s: %w", key, err)
		}
	}
	return tx.Commit()
}

func (s *Store) LoadMeta() (map[string]string, error) {
	if s == nil {
		return nil, errors.New("SQLite store 未初始化")
	}
	rows, err := s.db.Query(`SELECT key, value FROM meta`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	meta := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		meta[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return meta, nil
}

func loadKindRows(ctx context.Context, tx *sql.Tx, kind string) (map[int][]byte, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, data FROM entities WHERE kind = ?`, kind)
	if err != nil {
		return nil, fmt.Errorf("读取 %s: %w", kind, err)
	}
	defer rows.Close()
	existing := make(map[int][]byte)
	for rows.Next() {
		var id int
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return nil, fmt.Errorf("读取 %s/%d: %w", kind, id, err)
		}
		existing[id] = append([]byte(nil), data...)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取 %s: %w", kind, err)
	}
	return existing, nil
}

func (s *Store) LoadEntities() (map[string]map[int]json.RawMessage, error) {
	rows, err := s.db.Query(`SELECT kind, id, data FROM entities`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]map[int]json.RawMessage)
	for rows.Next() {
		var kind string
		var id int
		var data []byte
		if err := rows.Scan(&kind, &id, &data); err != nil {
			return nil, err
		}
		if result[kind] == nil {
			result[kind] = make(map[int]json.RawMessage)
		}
		result[kind][id] = append(json.RawMessage(nil), data...)
	}
	return result, rows.Err()
}

func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}

func (s *Store) Meta(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	return value, err
}

// Settings are the authoritative home for runtime-editable business
// configuration. The type column preserves the INI value kind so Beego-style
// boolean and numeric parsing keeps working after the value is injected.
func (s *Store) LoadSettings() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		values[key] = value
	}
	return values, rows.Err()
}

// UpsertSettings commits a related group of settings in one transaction. A
// panel save that changes SMTP and registration defaults must not leave half
// of the new configuration visible after a crash.
func (s *Store) UpsertSettings(values map[string]string) error {
	if s == nil {
		return errors.New("SQLite store 未初始化")
	}
	s.write.Lock()
	defer s.write.Unlock()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	for key, value := range values {
		if strings.TrimSpace(key) == "" {
			return errors.New("配置键不能为空")
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings(key, value, type, updated_at) VALUES (?, ?, ?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value, type = excluded.type, updated_at = excluded.updated_at`,
			key, value, settingValueType(value), now,
		); err != nil {
			return fmt.Errorf("写入配置 %s: %w", key, err)
		}
	}
	return tx.Commit()
}

// ReplaceSettings applies an upsert set and a delete set in one transaction.
// A registration form can both remove legacy directional aliases and write the
// canonical two-way bandwidth value; committing those halves separately would
// make an interrupted save expose a mixture of old and new limits.
func (s *Store) ReplaceSettings(values map[string]string, deleteKeys []string) error {
	if s == nil {
		return errors.New("SQLite store 未初始化")
	}
	if len(values) == 0 && len(deleteKeys) == 0 {
		return nil
	}
	s.write.Lock()
	defer s.write.Unlock()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" {
			return errors.New("配置键不能为空")
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings(key, value, type, updated_at) VALUES (?, ?, ?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value, type = excluded.type, updated_at = excluded.updated_at`,
			key, value, settingValueType(value), now,
		); err != nil {
			return fmt.Errorf("写入配置 %s: %w", key, err)
		}
	}
	for _, key := range deleteKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			return errors.New("配置键不能为空")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
			return fmt.Errorf("删除配置 %s: %w", key, err)
		}
	}
	return tx.Commit()
}

// CheckSettingsTable verifies that a newly opened database can read the
// settings table. Startup calls this before mutating nps.conf, so a corrupted
// database cannot leave the file stripped of the only copy of a setting.
func (s *Store) CheckSettingsTable() error {
	if s == nil {
		return errors.New("SQLite store 未初始化")
	}
	var count int
	return s.db.QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&count)
}

func settingValueType(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "true" || normalized == "false" {
		return "bool"
	}
	if _, err := strconv.ParseInt(normalized, 10, 64); err == nil {
		return "int"
	}
	if _, err := strconv.ParseFloat(normalized, 64); err == nil {
		return "float"
	}
	return "string"
}

// DeleteSettings removes keys in one transaction. Optional features that are
// disabled in the panel are deleted rather than stored as an empty override,
// so removing a setting and never having set it behave identically.
func (s *Store) DeleteSettings(keys []string) error {
	if s == nil {
		return errors.New("SQLite store 未初始化")
	}
	s.write.Lock()
	defer s.write.Unlock()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, key := range keys {
		if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
			return fmt.Errorf("删除配置 %s: %w", key, err)
		}
	}
	return tx.Commit()
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}
