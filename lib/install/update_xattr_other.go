//go:build !linux && !windows

package install

import "os"

func checkUpdateExtraPermissions(path string) error { return nil }
func captureUpdateExtraPermissions(path string) (func(*os.File) error, error) {
	return func(*os.File) error { return nil }, nil
}
