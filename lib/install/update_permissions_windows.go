//go:build windows

package install

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func checkUpdatePermissions(path string, info os.FileInfo) error {
	if info.Mode().Perm()&0200 == 0 {
		return fmt.Errorf("程序设置了只读属性，请由原安装账户手动更新")
	}
	return nil
}

func captureExecutablePermissions(path string) (func(*os.File) error, error) {
	flags := windows.SECURITY_INFORMATION(windows.OWNER_SECURITY_INFORMATION | windows.GROUP_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION)
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, flags)
	if err != nil {
		return nil, err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return nil, err
	}
	group, _, err := sd.Group()
	if err != nil {
		return nil, err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return nil, err
	}
	return func(f *os.File) error {
		// Freeze the effective ACL instead of inheriting a potentially wider
		// directory ACL. Do not take ownership or enable extra privileges.
		current, err := windows.GetNamedSecurityInfo(f.Name(), windows.SE_FILE_OBJECT, flags)
		if err != nil {
			return err
		}
		currentOwner, _, err := current.Owner()
		if err != nil {
			return err
		}
		currentGroup, _, err := current.Group()
		if err != nil {
			return err
		}
		setFlags := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
		if !windows.EqualSid(owner, currentOwner) {
			setFlags |= windows.OWNER_SECURITY_INFORMATION
		}
		if !windows.EqualSid(group, currentGroup) {
			setFlags |= windows.GROUP_SECURITY_INFORMATION
		}
		err = windows.SetNamedSecurityInfo(f.Name(), windows.SE_FILE_OBJECT, setFlags, owner, group, dacl, nil)
		if err != nil {
			return fmt.Errorf("无法保留程序所有者/访问控制列表，请由原安装账户更新：%w", err)
		}
		return nil
	}, nil
}
