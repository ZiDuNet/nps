package install

import (
	"fmt"
	"os"
	"path/filepath"
)

// PreflightUpdateTarget checks the current identity, without elevation and
// without modifying the installed program. Call before downloading or quitting.
// Permissions can change afterwards, so installation still checks every error.
func PreflightUpdateTarget(target, receipt string) error {
	if !filepath.IsAbs(target) {
		return fmt.Errorf("升级目标必须是绝对路径")
	}
	info, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("读取程序权限失败：%w", err)
	}
	if !info.Mode().IsRegular() && !(info.IsDir() && filepath.Ext(target) == ".app") {
		return fmt.Errorf("升级目标不是直接程序文件或 .app 应用包，请手动更新")
	}
	if err = checkUpdatePermissions(target, info); err != nil {
		return err
	}
	if info.Mode().IsRegular() {
		f, err := os.Open(target)
		if err != nil {
			return fmt.Errorf("当前账户不能读取原程序，无法安全备份：%w", err)
		}
		_ = f.Close()
	}
	permissions, err := captureExecutablePermissions(target)
	if err != nil {
		return fmt.Errorf("读取原程序访问权限失败：%w", err)
	}
	if err = probeUpdateDirectory(filepath.Dir(target), permissions); err != nil {
		return fmt.Errorf("当前账户不能安全替换 %s；请由原安装账户手动更新，不会自动提权：%w", target, err)
	}
	if receipt != "" {
		if !filepath.IsAbs(receipt) {
			return fmt.Errorf("升级回执必须使用绝对路径")
		}
		if info, err := os.Lstat(receipt); err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("升级回执不是普通文件")
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err = probeUpdateDirectory(filepath.Dir(receipt), nil); err != nil {
			return fmt.Errorf("升级回执目录不可写：%w", err)
		}
	}
	return nil
}

func probeUpdateDirectory(directory string, permissions func(*os.File) error) error {
	f, err := os.CreateTemp(directory, ".npc-preflight-*")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if permissions != nil {
		err = permissions(f)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	renamed := path + ".rename"
	if _, err = os.Lstat(renamed); !os.IsNotExist(err) {
		return fmt.Errorf("无法创建唯一的替换测试文件")
	}
	if err = os.Rename(path, renamed); err != nil {
		return err
	}
	return os.Remove(renamed)
}
