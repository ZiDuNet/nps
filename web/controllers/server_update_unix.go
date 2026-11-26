//go:build !windows

package controllers

import (
	"os"
	"syscall"
)

// execSelf replaces the current process image with the freshly installed
// binary. Re-exec keeps the PID, working directory and environment, so a
// systemd unit sees the same main process and does not need a restart job.
func execSelf(exe string) error {
	return syscall.Exec(exe, os.Args, os.Environ())
}
