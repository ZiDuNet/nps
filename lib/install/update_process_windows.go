//go:build windows

package install

import (
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func lockUpdateFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}

func ProcessRunning(pid int) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return err != windows.ERROR_INVALID_PARAMETER
	}
	defer windows.CloseHandle(h)
	status, err := windows.WaitForSingleObject(h, 0)
	return err != nil || status != windows.WAIT_OBJECT_0
}

func connectUpdateSCM(access uint32) (*mgr.Mgr, error) {
	h, err := windows.OpenSCManager(nil, nil, access)
	if err != nil {
		return nil, err
	}
	return &mgr.Mgr{Handle: h}, nil
}

func openUpdateService(m *mgr.Mgr, name string, access uint32) (*mgr.Service, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, err := windows.OpenService(m.Handle, p, access)
	if err != nil {
		return nil, err
	}
	return &mgr.Service{Name: name, Handle: h}, nil
}

func RunningServiceName(pid int, candidates ...string) (string, error) {
	m, err := connectUpdateSCM(windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "", err
	}
	defer m.Disconnect()
	for _, name := range candidates {
		s, err := openUpdateService(m, name, windows.SERVICE_QUERY_STATUS)
		if err != nil {
			continue
		}
		status, err := s.Query()
		s.Close()
		if err == nil && status.ProcessId == uint32(pid) {
			return name, nil
		}
	}
	// Legacy services may not persist -service_name. Enumerate only when the
	// supplied names did not match; neither path requests SCM_ALL_ACCESS.
	list, err := connectUpdateSCM(windows.SC_MANAGER_CONNECT | windows.SC_MANAGER_ENUMERATE_SERVICE)
	if err != nil {
		return "", fmt.Errorf("无法识别当前服务；请由原安装账户更新或保留 -service_name：%w", err)
	}
	defer list.Disconnect()
	names, err := list.ListServices()
	if err != nil {
		return "", err
	}
	for _, name := range names {
		s, err := openUpdateService(m, name, windows.SERVICE_QUERY_STATUS)
		if err != nil {
			continue
		}
		status, err := s.Query()
		s.Close()
		if err == nil && status.ProcessId == uint32(pid) {
			return name, nil
		}
	}
	return "", fmt.Errorf("未找到进程 %d 对应的 Windows 服务", pid)
}

func RegisteredServiceExecutable(name string) (string, error) {
	m, err := connectUpdateSCM(windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "", err
	}
	defer m.Disconnect()
	s, err := openUpdateService(m, name, windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		return "", err
	}
	defer s.Close()
	config, err := s.Config()
	if err != nil {
		return "", err
	}
	args, err := windows.DecomposeCommandLine(config.BinaryPathName)
	if err != nil || len(args) == 0 {
		return "", fmt.Errorf("服务 %s 程序路径无法解析", name)
	}
	return args[0], nil
}

func StopUpdateService(name string) error  { return controlUpdateService(name, false) }
func StartUpdateService(name string) error { return controlUpdateService(name, true) }

// PreflightUpdateService opens, but does not stop/start, the exact service with
// every permission needed for both update and recovery. Validate before quitting.
func PreflightUpdateService(name, target string, pid int) error {
	if name == "" {
		return nil
	}
	m, err := connectUpdateSCM(windows.SC_MANAGER_CONNECT)
	if err != nil {
		return fmt.Errorf("无法连接 Windows 服务管理器：%w", err)
	}
	defer m.Disconnect()
	s, err := openUpdateService(m, name, windows.SERVICE_QUERY_CONFIG|windows.SERVICE_QUERY_STATUS|windows.SERVICE_START|windows.SERVICE_STOP)
	if err != nil {
		return fmt.Errorf("当前账户缺少服务 %s 的查询/停止/启动权限；原服务保持运行，请由原安装账户手动更新：%w", name, err)
	}
	defer s.Close()
	if pid > 0 {
		status, err := s.Query()
		if err != nil || status.ProcessId != uint32(pid) {
			return fmt.Errorf("服务 %s 已不属于当前更新进程，本次未操作", name)
		}
	}
	config, err := s.Config()
	if err != nil {
		return err
	}
	args, err := windows.DecomposeCommandLine(config.BinaryPathName)
	if err != nil || len(args) == 0 || !strings.EqualFold(stableExecutablePath(args[0]), stableExecutablePath(target)) {
		return fmt.Errorf("服务 %s 的程序路径与升级目标不一致，请检查服务配置", name)
	}
	return nil
}

func controlUpdateService(name string, start bool) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("服务名称为空")
	}
	m, err := connectUpdateSCM(windows.SC_MANAGER_CONNECT)
	if err != nil {
		return err
	}
	defer m.Disconnect()
	access := uint32(windows.SERVICE_QUERY_STATUS | windows.SERVICE_STOP)
	if start {
		access = windows.SERVICE_QUERY_STATUS | windows.SERVICE_START
	}
	s, err := openUpdateService(m, name, access)
	if err != nil {
		return err
	}
	defer s.Close()
	ops := updateServiceOps{
		query: func() (updateServicePhase, error) {
			status, err := s.Query()
			if err != nil {
				return updateServiceOther, err
			}
			switch status.State {
			case svc.Stopped:
				return updateServiceStopped, nil
			case svc.Running:
				return updateServiceRunning, nil
			case svc.StartPending:
				return updateServiceStartPending, nil
			case svc.StopPending:
				return updateServiceStopPending, nil
			}
			return updateServiceOther, nil
		},
		start: func() error { return s.Start() },
		stop:  func() error { _, err := s.Control(svc.Stop); return err },
	}
	if err := ensureUpdateService(ops, start, 20*time.Second, 2*time.Second, 100*time.Millisecond); err != nil {
		return fmt.Errorf("服务 %s：%w", name, err)
	}
	return nil
}
