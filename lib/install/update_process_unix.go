//go:build !windows

package install

import (
	"errors"
	"os"
	"syscall"
)

func lockUpdateFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func ProcessRunning(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func StopUpdateService(name string) error {
	return errors.New("Unix 更新器不应停止系统服务")
}
func StartUpdateService(name string) error {
	return errors.New("Unix 系统服务应由原进程 exec 重启")
}

func RunningServiceName(pid int, candidates ...string) (string, error) { return "", nil }

func PreflightUpdateService(name, target string, pid int) error {
	if name != "" {
		return errors.New("Unix 更新器不应通过独立助手控制系统服务")
	}
	return nil
}
