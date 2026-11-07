package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"ehang.io/nps/lib/common"
)

const UpdatePlanFlag = "-nps_update_plan="
const UpdateReceiptEnv = "NPS_UPDATE_RECEIPT"
const updateStageMarker = "nps-updater-stage-v2\n"

// RestartPlan contains local paths and the original startup arguments only.
// It is saved with 0600 permissions inside a private staging directory.
type RestartPlan struct {
	Target           string                      `json:"target"`
	Source           string                      `json:"source"`
	Version          string                      `json:"version"`
	ParentPID        int                         `json:"parent_pid"`
	Service          string                      `json:"service,omitempty"`
	Args             []string                    `json:"args,omitempty"`
	WorkingDir       string                      `json:"working_dir"`
	Receipt          string                      `json:"receipt,omitempty"`
	Result           common.ClientUpdateProgress `json:"result"`
	MacBundle        string                      `json:"mac_bundle,omitempty"`
	BundleExecutable string                      `json:"bundle_executable,omitempty"`
	RequireStartup   bool                        `json:"require_startup,omitempty"`
}

func UpdatePlanArgument(args []string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, UpdatePlanFlag) {
			return strings.TrimPrefix(arg, UpdatePlanFlag)
		}
	}
	return ""
}

// StartUpdateHelper waits for the helper to acquire the installation lock.
// The caller may only stop/quit after this succeeds and ReleaseUpdateHelper is
// called, so helper startup failures never tear down a working client.
func StartUpdateHelper(directory string, plan RestartPlan) (string, error) {
	if err := validateRestartPlan(directory, plan); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(directory, ".nps-update-stage"), []byte(updateStageMarker), 0600); err != nil {
		return "", err
	}
	manifest, err := os.CreateTemp(directory, "restart-*.json")
	if err != nil {
		return "", err
	}
	planPath := manifest.Name()
	if err = manifest.Close(); err != nil {
		return "", err
	}
	if err = SecureUpdateFile(planPath); err != nil {
		return "", err
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(planPath, data, 0600); err != nil {
		return "", err
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	helper := filepath.Join(directory, "npc-updater-"+strings.TrimSuffix(strings.TrimPrefix(filepath.Base(planPath), "restart-"), ".json"))
	if common.IsWindows() {
		helper += ".exe"
	}
	if err = copyFileAtomic(exe, helper); err != nil {
		return "", err
	}
	if err = os.Chmod(helper, 0700); err != nil {
		return "", err
	}
	cmd := exec.Command(helper, UpdatePlanFlag+planPath)
	cmd.Dir = plan.WorkingDir
	if err = cmd.Start(); err != nil {
		return "", err
	}
	defer cmd.Process.Release()
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
		if payload, err := os.ReadFile(planPath + ".ready"); err == nil {
			if string(payload) != "ready" {
				return "", errors.New(string(payload))
			}
			return planPath, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", errors.New("更新器未就绪，原客户端保持运行")
}

func ReleaseUpdateHelper(planPath string) error {
	return os.WriteFile(planPath+".apply", []byte("apply"), 0600)
}

func ReadUpdateReceipt() *common.ClientUpdateProgress {
	path := os.Getenv(UpdateReceiptEnv)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var result common.ClientUpdateProgress
	if json.Unmarshal(data, &result) != nil {
		return nil
	}
	return &result
}

func WriteUpdateReceipt(path string, result common.ClientUpdateProgress) error {
	if path == "" {
		return nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return writeUpdateFile(path, data)
}

func writeUpdateFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".npc-receipt-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = SecureUpdateFile(f.Name()); err != nil {
		_ = f.Close()
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func validateRestartPlan(directory string, plan RestartPlan) error {
	if !filepath.IsAbs(directory) || !filepath.IsAbs(plan.Target) || !filepath.IsAbs(plan.Source) || !filepath.IsAbs(plan.WorkingDir) || plan.ParentPID < 0 {
		return errors.New("更新计划路径无效")
	}
	base := filepath.Base(directory)
	if !strings.HasPrefix(base, "nps-npc-update-") && !strings.HasPrefix(base, ".npc-gui-update-") {
		return errors.New("更新器只能使用专用暂存目录")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("更新暂存目录不能是符号链接")
	}
	if err := checkPrivateUpdateDirectory(directory); err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return err
	}
	source, err := filepath.EvalSymlinks(plan.Source)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(real, source)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("更新源不在本次暂存目录内")
	}
	if filepath.Clean(plan.Target) == filepath.Dir(filepath.Clean(plan.Target)) || filepath.Clean(plan.Target) == filepath.Clean(directory) {
		return errors.New("更新目标无效")
	}
	info, err = os.Stat(plan.Target)
	if err != nil {
		return err
	}
	if plan.MacBundle != "" {
		if plan.MacBundle != plan.Target || !info.IsDir() || !strings.HasSuffix(strings.ToLower(plan.Target), ".app") {
			return errors.New("应用包更新目标无效")
		}
		if plan.BundleExecutable == "" || filepath.IsAbs(plan.BundleExecutable) || filepath.Clean(plan.BundleExecutable) == ".." || strings.HasPrefix(filepath.Clean(plan.BundleExecutable), ".."+string(filepath.Separator)) {
			return errors.New("应用包程序路径无效")
		}
	} else if !info.Mode().IsRegular() {
		return errors.New("更新目标不是普通程序文件")
	}
	if err = PreflightUpdateTarget(plan.Target, plan.Receipt); err != nil {
		return err
	}
	return PreflightUpdateService(plan.Service, plan.Target, plan.ParentPID)
}

// RunUpdateHelper is used by both CLI and GUI before their normal entrypoints.
// Network access and package verification have already finished in the parent.
func RunUpdateHelper(planPath string) int {
	data, err := os.ReadFile(planPath)
	if err != nil {
		return 1
	}
	var plan RestartPlan
	base := filepath.Base(planPath)
	if json.Unmarshal(data, &plan) != nil || (base != "restart.json" && (!strings.HasPrefix(base, "restart-") || !strings.HasSuffix(base, ".json"))) {
		return 1
	}
	directory := filepath.Dir(planPath)
	marker, err := os.ReadFile(filepath.Join(directory, ".nps-update-stage"))
	if err != nil || string(marker) != updateStageMarker {
		return 1
	}
	if err = validateRestartPlan(directory, plan); err != nil {
		_ = os.WriteFile(planPath+".ready", []byte(err.Error()), 0600)
		return 1
	}
	if plan.MacBundle != "" {
		// Preserve the bundle's outer ownership/mode before the parent quits.
		// Resource permissions inside the new, verified bundle come from its
		// release archive; managed/custom-ACL applications require their installer.
		permissions, err := captureExecutablePermissions(plan.Target)
		if err == nil {
			var f *os.File
			f, err = os.Open(plan.Source)
			if err == nil {
				err = permissions(f)
				_ = f.Close()
			}
		}
		if err != nil {
			_ = os.WriteFile(planPath+".ready", []byte(err.Error()), 0600)
			return 1
		}
	}
	logFile, _ := os.OpenFile(filepath.Join(filepath.Dir(plan.Target), "npc-updater.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if logFile != nil {
		defer logFile.Close()
	}
	log := func(message string, args ...any) {
		if logFile != nil {
			fmt.Fprintf(logFile, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(message, args...))
		}
	}
	release, err := AcquireUpdateLock(plan.Target)
	if err != nil {
		_ = os.WriteFile(planPath+".ready", []byte(err.Error()), 0600)
		return 1
	}
	defer release()
	if err = os.WriteFile(planPath+".ready", []byte("ready"), 0600); err != nil {
		return 1
	}
	activated := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(planPath + ".apply"); err == nil {
			activated = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !activated {
		log("未收到切换确认，保留原进程")
		return 1
	}
	fail := func(err error) {
		log("升级失败：%v", err)
		plan.Result.Stage = "failed"
		plan.Result.Message = err.Error()
		_ = WriteUpdateReceipt(plan.Receipt, plan.Result)
		// Kept on failure so the still-running parent can report the error.
		_ = os.WriteFile(planPath+".failed", []byte(err.Error()), 0600)
	}
	if plan.Service != "" {
		if err = StopUpdateService(plan.Service); err != nil {
			fail(err)
			_ = StartUpdateService(plan.Service)
			return 1
		}
	}
	if err = WaitProcessExit(plan.ParentPID, 30*time.Second); err != nil {
		fail(err)
		if plan.Service != "" {
			_ = StartUpdateService(plan.Service)
		}
		return 1
	}
	var rollback func() error
	var commit func()
	if plan.MacBundle != "" {
		backupRoot, createErr := os.MkdirTemp(filepath.Dir(plan.Target), ".npc-app-rollback-*")
		if createErr != nil {
			fail(createErr)
			_ = startUpdatedClient(plan)
			return 1
		}
		backup := filepath.Join(backupRoot, filepath.Base(plan.Target))
		if err = os.Rename(plan.Target, backup); err == nil {
			err = os.Rename(plan.Source, plan.Target)
			if err != nil {
				_ = os.Rename(backup, plan.Target)
			}
		}
		rollback = func() error {
			failed := filepath.Join(directory, "failed.app")
			if err := os.Rename(plan.Target, failed); err != nil {
				return err
			}
			if err := os.Rename(backup, plan.Target); err != nil {
				return err
			}
			_ = os.RemoveAll(failed)
			_ = os.Remove(backupRoot)
			return nil
		}
		commit = func() { _ = os.RemoveAll(backupRoot) }
		if err != nil {
			_ = os.Remove(backupRoot)
		}
	} else {
		var tx *BinaryTransaction
		tx, err = InstallBinary(plan.Source, plan.Target)
		if err == nil {
			rollback, commit = tx.Rollback, tx.Commit
		}
	}
	if err != nil {
		fail(err)
		_ = startUpdatedClient(plan)
		return 1
	}
	plan.Result.Stage = "restarted"
	plan.Result.Version = plan.Version
	plan.Result.Message = "新程序已启动，等待连接确认"
	err = WriteUpdateReceipt(plan.Receipt, plan.Result)
	if err == nil {
		err = startUpdatedClient(plan)
	}
	if err != nil {
		fail(fmt.Errorf("新程序启动失败：%w", err))
		if plan.Service != "" {
			_ = StopUpdateService(plan.Service)
		}
		if restoreErr := rollback(); restoreErr != nil {
			log("回滚失败：%v", restoreErr)
			return 1
		}
		if restartErr := startUpdatedClient(plan); restartErr != nil {
			log("恢复旧程序启动失败：%v", restartErr)
		}
		return 1
	}
	commit()
	log("程序切换与启动完成，版本=%s，服务=%s；上线结果由服务端确认", plan.Version, plan.Service)
	// directory was validated against a private-stage name, marker and source
	// containment before any action. Never remove a manifest's arbitrary parent.
	_ = os.RemoveAll(directory)
	return 0
}

// A service parent is normally terminated by SCM. If stopping it fails, the
// helper leaves a failure file and the live parent reports that instead of
// permanently retaining its in-process update guard.
func WaitUpdateHelperHandoff(planPath string) error {
	for deadline := time.Now().Add(40 * time.Second); time.Now().Before(deadline); {
		if data, err := os.ReadFile(planPath + ".failed"); err == nil {
			return errors.New(string(data))
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("更新器未能结束原服务，原进程仍在运行；请检查 npc-updater.log")
}

func startUpdatedClient(plan RestartPlan) error {
	if plan.Result.Stage == "failed" {
		plan.Version = ""
	}
	if plan.Service != "" {
		if err := StartUpdateService(plan.Service); err != nil {
			return err
		}
		if plan.RequireStartup {
			return waitUpdateStartup(plan)
		}
		return nil
	}
	cmd := exec.Command(plan.Target, plan.Args...)
	if plan.MacBundle != "" {
		// Launch the real bundle executable, not the short-lived `open`
		// process. This preserves environment, exposes early exit, and lets
		// us terminate only this new process before rollback.
		cmd = exec.Command(filepath.Join(plan.Target, plan.BundleExecutable), plan.Args...)
	}
	cmd.Dir = plan.WorkingDir
	cmd.Env = append(os.Environ(), UpdateReceiptEnv+"="+plan.Receipt)
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	started := time.Now()
	for time.Since(started) < 10*time.Second {
		select {
		case err := <-done:
			return fmt.Errorf("程序启动后立即退出：%v", err)
		case <-time.After(100 * time.Millisecond):
			if time.Since(started) >= 2*time.Second && (!plan.RequireStartup || updateStartupConfirmed(plan.Receipt, plan.Version)) {
				return nil
			}
		}
	}
	_ = cmd.Process.Kill()
	<-done
	return errors.New("新程序未完成启动初始化，已结束该进程并准备回滚")
}

func waitUpdateStartup(plan RestartPlan) error {
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if updateStartupConfirmed(plan.Receipt, plan.Version) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("服务启动后未收到目标版本初始化确认")
}

func AcquireUpdateLock(target string) (func(), error) {
	lock := stableExecutablePath(target) + ".update-lock"
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("安装目录不可写：%w", err)
	}
	if err = lockUpdateFile(f); err != nil {
		_ = f.Close()
		return nil, errors.New("此程序已有其他升级任务，原连接保持运行")
	}
	// Keep the lock inode. Unlinking it allows a second process to create a
	// different inode while a third process still holds the original lock.
	// os.OpenFile descriptors are close-on-exec, including the Unix PID-preserving restart.
	return func() { _ = f.Close() }, nil
}

func WaitProcessExit(pid int, timeout time.Duration) error {
	if pid <= 0 {
		return nil
	}
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		if !ProcessRunning(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("等待原进程 %d 退出超时，未替换程序", pid)
}
