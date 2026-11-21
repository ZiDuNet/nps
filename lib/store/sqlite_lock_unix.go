//go:build !windows

package store

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockDatabaseFile takes and holds an exclusive advisory lock for the lifetime
// of the process. NPS keeps all authoritative data in memory, so two
// processes writing the same SQLite file would silently overwrite one
// another's snapshots. The lock is deliberately separate from the database's
// own locking: the file stays open for the whole process lifetime and is
// released by the kernel if the process exits unexpectedly.
func lockDatabaseFile(file *os.File) error {
	if file == nil {
		return ErrDatabaseLocked
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			return ErrDatabaseLocked
		}
		return err
	}
	return nil
}
