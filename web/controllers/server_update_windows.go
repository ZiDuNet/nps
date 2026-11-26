//go:build windows

package controllers

import "errors"

// execSelf is never reached on Windows because detectServerUpdateCapability
// refuses the upgrade: a running executable cannot be replaced there.
func execSelf(string) error {
	return errors.New("Windows 不支持在面板内重启到新程序")
}
