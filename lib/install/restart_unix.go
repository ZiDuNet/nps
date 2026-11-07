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
	return RestartNpcProcessWithArgs(os.Args[1:])
}

func RestartNpcProcessWithArgs(args []string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	// The updater keeps a Windows backup as <binary>.old. Older Unix updates
	// could create the same backup and leave /proc/self/exe pointing at it;
	// always restart the stable installation path instead of that backup.
	executable = stableExecutablePath(executable)
	return syscall.Exec(executable, append([]string{executable}, args...), os.Environ())
}
