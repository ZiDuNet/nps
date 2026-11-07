//go:build windows

package install

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

func privateUpdateSecurity(directory bool) (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	inherit := ""
	if directory {
		inherit = "OICI"
	}
	return windows.SecurityDescriptorFromString(fmt.Sprintf("D:P(A;%s;FA;;;SY)(A;%s;FA;;;%s)", inherit, inherit, user.User.Sid.String()))
}

func secureUpdatePath(path string, directory bool) error {
	sd, err := privateUpdateSecurity(directory)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func checkPrivateUpdateDirectory(path string) error {
	expected, err := privateUpdateSecurity(true)
	if err != nil {
		return err
	}
	actual, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	control, _, err := actual.Control()
	if err != nil {
		return err
	}
	// Windows may keep harmless AI/AR descriptor flags after removing inherited
	// ACEs. Compare the actual ACEs and require inheritance to be protected.
	_, actualACEs, actualOK := strings.Cut(actual.String(), "(")
	_, expectedACEs, expectedOK := strings.Cut(expected.String(), "(")
	if control&windows.SE_DACL_PROTECTED == 0 || !actualOK || !expectedOK || actualACEs != expectedACEs {
		return errors.New("更新暂存目录不属于当前账户的私有目录，请重新下载更新")
	}
	return nil
}
