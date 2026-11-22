package durablefile

import (
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// Rename asks Windows to write through the rename; FlushFileBuffers does not
// support ordinary directory handles the way fsync does on Unix.
func Rename(source, destination string) error {
	from, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	to, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	fromPath, err := syscall.UTF16PtrFromString(windowsLongPath(from))
	if err != nil {
		return err
	}
	toPath, err := syscall.UTF16PtrFromString(windowsLongPath(to))
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(fromPath, toPath, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return &os.LinkError{Op: "rename", Old: source, New: destination, Err: err}
	}
	return nil
}

func windowsLongPath(path string) string {
	if len(path) < 248 || path[:4] == `\\?\` {
		return path
	}
	if path[:2] == `\\` {
		return `\\?\UNC\` + path[2:]
	}
	return `\\?\` + path
}

// On Windows publication uses a write-through rename instead of a separate
// directory fsync. An interrupted deletion can safely be retried.
func SyncDir(string) error { return nil }
