//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detachProcess starts the helper in its own session so closing the terminal
// that ran the foreground command cannot interrupt an in-flight update.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
