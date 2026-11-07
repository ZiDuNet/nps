//go:build !windows

package install

import (
	"errors"
	"os"
	"syscall"
)

func secureUpdatePath(path string, directory bool) error {
	mode := os.FileMode(0600)
	if directory {
		mode = 0700
	}
	return os.Chmod(path, mode)
}

func checkPrivateUpdateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0077 != 0 {
		return errors.New("更新暂存目录必须由当前账户独占，请重新下载更新")
	}
	return nil
}
