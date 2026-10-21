//go:build !windows

package install

import (
	"os"
	"syscall"
)

// RestartNpcProcess replaces the current process with the updated executable.
// This preserves the PID and service-manager supervision while reopening all
// runtime state from the newly installed binary.
func RestartNpcProcess() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(executable, os.Args, os.Environ())
}
