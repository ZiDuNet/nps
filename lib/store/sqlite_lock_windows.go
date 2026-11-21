//go:build windows

package store

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockDatabaseFile(file *os.File) error {
	if file == nil {
		return ErrDatabaseLocked
	}
	overlapped := new(windows.Overlapped)
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, overlapped,
	)
	if err != nil {
		if err == windows.ERROR_LOCK_VIOLATION {
			return ErrDatabaseLocked
		}
		return err
	}
	return nil
}
