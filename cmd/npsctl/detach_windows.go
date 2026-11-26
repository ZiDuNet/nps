//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

const detachedProcessFlag = 0x00000008

// detachProcess keeps the helper independent from the console of the command
// that spawned it, so it survives the parent exiting immediately.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcessFlag}
}
