//go:build !windows

package durablefile

import (
	"errors"
	"os"
	"path/filepath"
)

// Rename publishes a file whose contents have already been synced. The
// destination entry must be durable before a caller removes its old data.
func Rename(source, destination string) error {
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	return SyncDir(filepath.Dir(destination))
}

// SyncDir makes pending changes to directory entries durable.
func SyncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
