//go:build !windows

package install

import (
	"fmt"
	"os"
	"syscall"
)

func checkUpdatePermissions(path string, info os.FileInfo) error {
	if info.IsDir() {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != os.Geteuid() {
			return fmt.Errorf("应用包不属于当前运行账户，请退出后由原安装账户更新；不会以其他账户重新创建应用包")
		}
	}
	if info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return fmt.Errorf("原程序设置了 setuid/setgid，请由管理员手动更新并复核权限")
	}
	return checkUpdateExtraPermissions(path)
}

func captureExecutablePermissions(path string) (func(*os.File) error, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("无法确认原程序的属主和属组")
	}
	extra, err := captureUpdateExtraPermissions(path)
	if err != nil {
		return nil, err
	}
	return func(f *os.File) error {
		current, err := f.Stat()
		if err != nil {
			return err
		}
		owner, ok := current.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("无法确认新文件的属主和属组")
		}
		if owner.Uid != stat.Uid || owner.Gid != stat.Gid {
			if err = f.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
				return fmt.Errorf("无法保留原程序属主/属组；请由原安装账户更新：%w", err)
			}
		}
		if err = f.Chmod(info.Mode().Perm()); err != nil {
			return err
		}
		return extra(f)
	}, nil
}
