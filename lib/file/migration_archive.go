package file

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ehang.io/nps/lib/durablefile"
	"ehang.io/nps/lib/settings"
	"ehang.io/nps/lib/version"
)

const migrationBackupFolder = "backups"

type migrationArchive struct {
	name   string
	digest string
	files  map[string]string // SHA-256 of each regular archive entry
}

func createMigrationArchive(runPath, staging string) (migrationArchive, error) {
	conf := filepath.Join(runPath, "conf")
	source, err := filepath.EvalSymlinks(conf)
	if err != nil {
		return migrationArchive{}, err
	}
	backupDir := filepath.Join(source, migrationBackupFolder)
	if err := os.Mkdir(backupDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return migrationArchive{}, err
	}
	info, err := os.Lstat(backupDir)
	if err != nil || !info.IsDir() {
		return migrationArchive{}, fmt.Errorf("迁移备份目录不可用: %s", backupDir)
	}
	f, err := os.CreateTemp(backupDir, ".pre-sqlite-"+time.Now().Format("20060102-150405")+"-*.tmp")
	if err != nil {
		return migrationArchive{}, err
	}
	defer os.Remove(f.Name())
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	original := make(map[string]string)
	archiveErr := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == migrationBackupFolder || strings.HasPrefix(rel, ".sqlite-init-") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == "nps.db.lock" {
			return nil
		}
		digest, err := appendConfArchiveEntry(tw, path, "conf/"+filepath.ToSlash(rel), entry)
		if err == nil && digest != "" {
			original["conf/"+filepath.ToSlash(rel)] = digest
		}
		return err
	})
	if err := errors.Join(archiveErr, tw.Close(), gz.Close(), f.Sync(), f.Close()); err != nil {
		return migrationArchive{}, err
	}
	archive, err := readMigrationArchive(f.Name(), "", staging)
	if err != nil {
		return migrationArchive{}, err
	}
	if !maps.Equal(archive.files, original) {
		return migrationArchive{}, errors.New("迁移备份读回校验不一致")
	}
	archive.name = strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f.Name()), "."), ".tmp") + ".tar.gz"
	if err := durablefile.Rename(f.Name(), filepath.Join(backupDir, archive.name)); err != nil {
		return migrationArchive{}, err
	}
	if err := durablefile.SyncDir(source); err != nil {
		return migrationArchive{}, err
	}
	return archive, nil
}

func (archive migrationArchive) sync(runPath string) error {
	path := archive.path(runPath)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	return errors.Join(durablefile.SyncDir(dir), durablefile.SyncDir(filepath.Dir(dir)))
}

func appendConfArchiveEntry(tw *tar.Writer, path, name string, entry os.DirEntry) (string, error) {
	info, err := entry.Info()
	if err != nil {
		return "", err
	}
	var link string
	if info.Mode()&os.ModeSymlink != 0 {
		// Include the actual contents of symlinked configuration and TLS
		// files, so rollback does not depend on mutable external files.
		if target, statErr := os.Stat(path); statErr == nil && target.Mode().IsRegular() {
			info = target
		} else if link, err = os.Readlink(path); err != nil {
			return "", err
		}
	}
	if !info.Mode().IsRegular() && !info.IsDir() && link == "" {
		return "", fmt.Errorf("无法备份非普通配置文件 %s", name)
	}
	header, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return "", err
	}
	header.Name = name
	if info.IsDir() {
		header.Name += "/"
	}
	if err := tw.WriteHeader(header); err != nil || !info.Mode().IsRegular() {
		return "", err
	}
	input, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(tw, hash), input)
	if err := errors.Join(err, input.Close()); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// A restart verifies the same archive, not whatever happens to be in conf
// at that time. Only known JSON files are extracted, into private staging.
func readMigrationArchive(path, expected, staging string) (migrationArchive, error) {
	input, err := os.Open(path)
	if err != nil {
		return migrationArchive{}, err
	}
	defer input.Close()
	hash := sha256.New()
	source := io.TeeReader(input, hash)
	gz, err := gzip.NewReader(source)
	if err != nil {
		return migrationArchive{}, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	archive := migrationArchive{name: filepath.Base(path), files: make(map[string]string)}
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return migrationArchive{}, err
		}
		name := strings.TrimPrefix(header.Name, "conf/")
		importFile := staging != "" && header.Name == "conf/"+name && isLegacyImportFile(name)
		if header.Typeflag != tar.TypeReg {
			if importFile {
				return migrationArchive{}, fmt.Errorf("旧数据 %s 不是可读取的普通文件", name)
			}
			continue
		}
		if _, duplicate := archive.files[header.Name]; duplicate {
			return migrationArchive{}, fmt.Errorf("迁移备份含重复文件: %s", header.Name)
		}
		entryHash := sha256.New()
		if importFile {
			content, err := io.ReadAll(tr)
			if err != nil {
				return migrationArchive{}, err
			}
			_, _ = entryHash.Write(content)
			if name == jsonGatewayFile || name == gatewayFileUsers || !isPlaceholderJSONData(name, content) {
				if err := os.WriteFile(filepath.Join(staging, "conf", name), content, 0o600); err != nil {
					return migrationArchive{}, err
				}
			}
		} else if _, err := io.Copy(entryHash, tr); err != nil {
			return migrationArchive{}, err
		}
		archive.files[header.Name] = fmt.Sprintf("%x", entryHash.Sum(nil))
	}
	// Read to the gzip footer, so truncation and checksum errors are detected.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return migrationArchive{}, err
	}
	if _, err := io.Copy(io.Discard, source); err != nil {
		return migrationArchive{}, err
	}
	archive.digest = fmt.Sprintf("%x", hash.Sum(nil))
	if expected != "" && archive.digest != expected {
		return migrationArchive{}, errors.New("迁移备份内容已变化，拒绝清理原始文件")
	}
	return archive, nil
}

func isLegacyImportFile(name string) bool {
	switch name {
	case gatewayFileClients, gatewayFileUsers, gatewayFileTasks, gatewayFileHosts, gatewayFileGlobal, jsonGatewayFile:
		return true
	}
	return false
}

func isLegacyMigrationArtifact(name string) bool {
	if isLegacyImportFile(name) || name == jsonGatewayFile+".bak" {
		return true
	}
	base, suffix, ok := strings.Cut(name, ".bak.")
	if !ok {
		return false
	}
	if base == "nps.conf.pre-settings" {
		release, timestamp, ok := strings.Cut(suffix, ".20")
		_, versionErr := version.Compare(release, release)
		_, dateErr := time.Parse("20060102-150405", "20"+timestamp)
		return ok && versionErr == nil && dateErr == nil
	}
	if _, err := version.Compare(suffix, suffix); err != nil {
		return false
	}
	base = strings.TrimSuffix(base, ".pre-gateway")
	return isLegacyImportFile(base) && base != jsonGatewayFile
}

// After a crash the file may be either original or already slimmed. Never
// discard business settings that were added or changed after the backup.
func (archive migrationArchive) checkConfigForCleanup(runPath string, ini settings.StartupINI, stored map[string]string) error {
	if _, changed := settings.SlimStartupConfig(ini.Content, stored, ini.RunMode); !changed {
		return nil
	}
	if _, err := os.Lstat(filepath.Join(runPath, "conf", "nps.conf")); errors.Is(err, os.ErrNotExist) && archive.files["conf/nps.conf"] == "" {
		return nil // the embedded first-run template has not been written yet
	}
	if fmt.Sprintf("%x", sha256.Sum256([]byte(ini.Content))) != archive.files["conf/nps.conf"] {
		return errors.New("nps.conf 在迁移备份后发生变化；为保留变更，请检查原配置和 SQLite 设置后重试")
	}
	return nil
}

func (archive migrationArchive) cleanup(runPath string) error {
	conf := filepath.Join(runPath, "conf")
	entries, err := os.ReadDir(conf)
	if err != nil {
		return err
	}
	var removable []string
	for _, entry := range entries {
		name := entry.Name()
		if !isLegacyMigrationArtifact(name) {
			continue
		}
		expected := archive.files["conf/"+name]
		if expected == "" {
			continue // newly added or unarchived files are not ours to delete
		}
		path := filepath.Join(conf, name)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("旧文件 %s 无法校验，已保留", name)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if fmt.Sprintf("%x", sha256.Sum256(content)) != expected {
			return fmt.Errorf("旧文件 %s 在备份后发生变化，已保留", name)
		}
		removable = append(removable, path)
	}
	for _, path := range removable {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
