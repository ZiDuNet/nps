//go:build linux

package install

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func missingUpdateXattr(err error) bool {
	return errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP)
}

func checkUpdateExtraPermissions(path string) error {
	// These signatures/capabilities cannot safely be copied onto new code.
	// Refuse rather than silently removing them or granting the new code privilege.
	for _, name := range []string{"security.capability", "security.ima", "security.evm"} {
		size, err := unix.Getxattr(path, name, nil)
		if err == nil && size > 0 {
			return fmt.Errorf("原程序包含 %s，需管理员手动更新并重新核验；文件能力可改用 systemd AmbientCapabilities 管理", name)
		}
		if err != nil && !missingUpdateXattr(err) {
			return fmt.Errorf("无法检查 %s：%w", name, err)
		}
	}
	return nil
}

func captureUpdateExtraPermissions(path string) (func(*os.File) error, error) {
	attributes := make(map[string][]byte)
	for _, name := range []string{"system.posix_acl_access", "security.selinux"} {
		size, err := unix.Getxattr(path, name, nil)
		if missingUpdateXattr(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		value := make([]byte, size)
		n, err := unix.Getxattr(path, name, value)
		if err != nil {
			return nil, err
		}
		attributes[name] = value[:n]
	}
	return func(f *os.File) error {
		if _, exists := attributes["system.posix_acl_access"]; !exists {
			// Do not acquire a wider named-user ACL from the install directory.
			if err := unix.Fremovexattr(int(f.Fd()), "system.posix_acl_access"); err != nil && !missingUpdateXattr(err) {
				return err
			}
		}
		for name, value := range attributes {
			existing := make([]byte, len(value))
			n, err := unix.Fgetxattr(int(f.Fd()), name, existing)
			if err == nil && bytes.Equal(existing[:n], value) {
				continue
			}
			if err = unix.Fsetxattr(int(f.Fd()), name, value, 0); err != nil {
				return fmt.Errorf("无法保留原程序 %s：%w", name, err)
			}
		}
		return nil
	}, nil
}
