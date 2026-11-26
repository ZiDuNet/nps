package install

import (
	"os"
	"path/filepath"
)

// ServerUpdateBackupSuffix marks the rollback copy written by a panel-driven
// server upgrade. It is deliberately different from the menu updater's
// ".pre-update" file so the two entrypoints never delete each other's backup.
const ServerUpdateBackupSuffix = ".pre-web-update"

// AcknowledgeServerUpdateStartup removes the panel upgrade backup once the new
// binary has actually started. A failed re-exec never reaches this point, so
// the rollback copy stays available for manual recovery.
func AcknowledgeServerUpdateStartup() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	exe = stableExecutablePath(filepath.Clean(exe))
	if exe == "" {
		return
	}
	backup := exe + ServerUpdateBackupSuffix
	info, err := os.Stat(backup)
	if err != nil || info.IsDir() {
		return
	}
	_ = os.Remove(backup)
}
