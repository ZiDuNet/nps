//go:build windows

package install

import (
	"os"
	"os/exec"
)

// RestartNpcProcess starts the replaced executable with the same arguments,
// then exits this process. Windows services use the service manager instead.
func RestartNpcProcess() error {
	return RestartNpcProcessWithArgs(os.Args[1:])
}

func RestartNpcProcessWithArgs(args []string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable = stableExecutablePath(executable)
	cmd := exec.Command(executable, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
