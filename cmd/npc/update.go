//go:build !sdk

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/client"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/install"
	"ehang.io/nps/lib/quickcommand"
	"ehang.io/nps/lib/version"
	"github.com/kardianos/service"
)

var runtimeCommands struct {
	sync.Mutex
	items []quickcommand.Command
}

func containerNpc() bool {
	if os.Getenv("NPS_CONTAINER") == "1" {
		return true
	}
	_, err := os.Stat("/.dockerenv")
	if err == nil {
		return true
	}
	_, err = os.Stat("/run/.containerenv")
	return err == nil || os.Getenv("container") != ""
}

func npcExecutable() (string, error) {
	exe, err := os.Executable()
	return install.StableExecutablePath(exe), err
}

func npcReceiptPath() string { exe, _ := npcExecutable(); return exe + ".update-result.json" }

func loadNpcUpdateReceipt() {
	exe, _ := npcExecutable()
	install.FinalizeExecUpdateReceipt(npcReceiptPath(), exe, version.VERSION)
	install.AcknowledgeUpdateStartup(npcReceiptPath(), version.VERSION)
	data, err := os.ReadFile(npcReceiptPath())
	if err != nil {
		return
	}
	var result common.ClientUpdateProgress
	if json.Unmarshal(data, &result) == nil {
		client.SetUpdateResult(&result)
	}
}

func performNpcRemoteUpdate(ctx client.UpdateContext) (bool, error) {
	target, err := npcExecutable()
	if err != nil {
		return false, err
	}
	if err = install.PreflightUpdateTarget(target, npcReceiptPath()); err != nil {
		return false, err
	}
	name := ""
	if runtime.GOOS == "windows" && !service.Interactive() {
		name, err = install.RunningServiceName(os.Getpid(), *serviceName, "nps-client-"+ctx.VKey)
		if err != nil {
			return false, err
		}
		if err = install.PreflightUpdateService(name, target, os.Getpid()); err != nil {
			return false, err
		}
	}
	pkg, err := install.PrepareNpcUpdate(ctx.Version, false, ctx.HTTPClient, ctx.Report)
	if err != nil || pkg == nil {
		return false, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			pkg.Cleanup()
		}
	}()
	args, err := npcRestartArgs()
	if err != nil {
		return false, err
	}
	result := common.ClientUpdateProgress{JobID: ctx.JobID, Stage: "restarted", Version: pkg.Version}
	if runtime.GOOS == "windows" {
		cwd, _ := os.Getwd()
		plan := install.RestartPlan{Target: target, Source: pkg.Binary, Version: pkg.Version, ParentPID: os.Getpid(), Service: name, Args: args, WorkingDir: cwd, Receipt: npcReceiptPath(), Result: result, RequireStartup: true}
		planPath, err := install.StartUpdateHelper(pkg.Directory, plan)
		if err != nil {
			return false, err
		}
		ctx.Report("restarting", pkg.Version, "更新包已就绪，即将切换程序；同文件的其他服务不会被自动重启")
		if err = install.ReleaseUpdateHelper(planPath); err != nil {
			return false, err
		}
		cleanup = false
		if name == "" {
			os.Exit(0)
		}
		// A successful service handoff terminates this process. Returning an
		// error here means it stayed alive and must release its update guard.
		return false, install.WaitUpdateHelperHandoff(planPath)
	}
	release, err := install.AcquireUpdateLock(target)
	if err != nil {
		return false, err
	}
	defer release()
	ctx.Report("installing", pkg.Version, "更新包已校验，正在原子替换程序")
	tx, err := install.InstallBinary(pkg.Binary, target)
	if err != nil {
		return false, err
	}
	if err = install.WriteExecUpdateReceipt(npcReceiptPath(), result, tx.Backup); err != nil {
		_ = tx.Rollback()
		return false, err
	}
	ctx.Report("restarting", pkg.Version, "正在重启原进程并恢复连接")
	// Exec preserves the service manager's PID, startup parameters and cwd.
	pkg.Cleanup()
	err = install.RestartNpcProcessWithArgs(args)
	if err != nil {
		restoreErr := tx.Rollback()
		result.Stage = "failed"
		result.Message = fmt.Sprintf("自动重启失败：%v", err)
		_ = install.WriteUpdateReceipt(npcReceiptPath(), result)
		return false, fmt.Errorf("自动重启失败：%v；回滚结果：%v", err, restoreErr)
	}
	return true, nil
}

func rememberRuntimeCommand(command quickcommand.Command) {
	runtimeCommands.Lock()
	defer runtimeCommands.Unlock()
	for _, item := range runtimeCommands.items {
		if slices.Equal(item.Args(), command.Args()) {
			return
		}
	}
	runtimeCommands.items = append(runtimeCommands.items, command)
}

func npcRestartArgs() ([]string, error) {
	args := make([]string, 0, len(os.Args))
	for _, arg := range os.Args[1:] {
		if !strings.HasPrefix(arg, "-resume_commands=") {
			args = append(args, arg)
		}
	}
	runtimeCommands.Lock()
	commands := append([]quickcommand.Command(nil), runtimeCommands.items...)
	runtimeCommands.Unlock()
	if len(commands) == 0 {
		return args, nil
	}
	f, err := os.CreateTemp("", "npc-resume-*.json")
	if err != nil {
		return nil, err
	}
	if err = install.SecureUpdateFile(f.Name()); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, err
	}
	if err = json.NewEncoder(f).Encode(commands); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	return append(args, "-resume_commands="+f.Name()), nil
}

func readResumeCommands(path string) ([]quickcommand.Command, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var commands []quickcommand.Command
	if err = json.Unmarshal(data, &commands); err != nil {
		return nil, err
	}
	for i := range commands {
		commands[i], err = quickcommand.Normalize(commands[i])
		if err != nil {
			return nil, err
		}
	}
	// Retain briefly so a failed new process can be rolled back with exactly
	// the same menu connections. No permanent plaintext credential store.
	go func() { time.Sleep(30 * time.Second); _ = os.Remove(path) }()
	return commands, nil
}

// Explicit menu/manual update affects only services selected by the local
// operator. Remote updates never restart neighbouring VKey services.
func updateNpcManually(force bool) error {
	return updateNpcManuallyFrom(force, bufio.NewReader(os.Stdin))
}

func updateNpcManuallyFrom(force bool, input io.Reader) error {
	if containerNpc() {
		return errors.New("容器请拉取新镜像并重建，不在容器内部覆盖程序")
	}
	services, err := installedNpcUpdateServices()
	if err != nil {
		return err
	}
	restart := false
	for _, item := range services {
		fmt.Printf("服务 %s：%s（%s）\n", item.Name, item.Path, serviceStatusText(item.Status, nil))
		if item.Status == service.StatusRunning {
			restart = true
		}
	}
	if restart {
		fmt.Print("更新后重启以上运行中的服务？会短暂中断这些隧道 (y/N)：")
		var confirmation string
		_, _ = fmt.Fscanln(input, &confirmation)
		restart = strings.EqualFold(strings.TrimSpace(confirmation), "y")
	}
	self, err := npcExecutable()
	if err != nil {
		return err
	}
	targets := map[string][]npcUpdateService{self: nil}
	for _, item := range services {
		targets[item.Path] = append(targets[item.Path], item)
	}
	// Fail before downloading or changing any installed copy when permissions
	// are insufficient. A declined restart must never require service-control rights.
	for target, items := range targets {
		if err := install.PreflightUpdateTarget(target, ""); err != nil {
			return err
		}
		if restart && runtime.GOOS == "windows" {
			for _, item := range items {
				if item.Status == service.StatusRunning {
					if err := install.PreflightUpdateService(item.Name, target, 0); err != nil {
						return err
					}
				}
			}
		}
	}
	httpClient := client.UpdateHTTPClient(*proxyUrl, *localIP)
	defer httpClient.CloseIdleConnections()
	pkg, err := install.PrepareNpcUpdate("", force || len(services) > 0, httpClient, func(stage, release, message string) { fmt.Println(message) })
	if err != nil {
		return err
	}
	if pkg == nil {
		fmt.Println("当前已是最新版本")
		return nil
	}
	defer pkg.Cleanup()
	var failures []error
	for target, items := range targets {
		if !force {
			if target == self {
				if cmp, err := version.Compare(version.VERSION, pkg.Version); err == nil && cmp > 0 {
					continue
				}
			}
			// A fresh menu binary can repair an older service copy even when the
			// menu itself is current. Never downgrade an already-newer copy.
			if release, err := install.NpcBinaryVersion(target); err == nil {
				if cmp, err := version.Compare(release, pkg.Version); err == nil && cmp > 0 {
					continue
				}
			}
		}
		release, err := install.AcquireUpdateLock(target)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		err = applyManualNpcUpdate(pkg.Binary, target, items, restart, controlNpcService)
		release()
		if err != nil {
			failures = append(failures, fmt.Errorf("%s：%w", target, err))
			continue
		}
		fmt.Println("已更新：", target)
	}
	if !restart && len(services) > 0 {
		fmt.Println("程序已更新，但未重启服务；运行版本暂时不变，可通过菜单选项 9 重启。")
	}
	fmt.Println("当前菜单进程仍是原版本；关闭后重新打开即可使用新版菜单。")
	return errors.Join(failures...)
}

type npcUpdateService struct {
	Name, Path string
	Status     service.Status
}

func installedNpcUpdateServices() ([]npcUpdateService, error) {
	names, err := listNpcServiceNames()
	if err != nil {
		return nil, err
	}
	if npcServiceInstalled("Npc") {
		names = append(names, "Npc")
	}
	var result []npcUpdateService
	for _, name := range names {
		path, err := install.RegisteredServiceExecutable(name)
		if err != nil {
			return nil, err
		}
		if real, err := filepath.EvalSymlinks(path); err == nil {
			path = real
		}
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("服务 %s 路径无效", name)
		}
		// A custom wrapper may register /bin/sh or another interpreter. Never
		// overwrite that executable just because its service name looks like NPC.
		if _, err := install.NpcBinaryVersion(path); err != nil {
			return nil, fmt.Errorf("服务 %s 不是可确认的 NPC 直接启动配置，请手动更新：%w", name, err)
		}
		status, err := npcServiceStatus(name)
		if err != nil {
			return nil, fmt.Errorf("读取服务 %s 状态失败：%w", name, err)
		}
		result = append(result, npcUpdateService{Name: name, Path: install.StableExecutablePath(path), Status: status})
	}
	return result, nil
}

func applyManualNpcUpdate(source, target string, services []npcUpdateService, restart bool, control func(string, string) error) error {
	tx, err := install.InstallBinary(source, target)
	if err != nil {
		return err
	}
	if restart {
		for _, item := range services {
			if item.Status != service.StatusRunning {
				continue
			}
			if err = control(item.Name, "restart"); err != nil {
				restoreErr := tx.Rollback()
				var recovery []error
				for _, previous := range services {
					if previous.Status == service.StatusRunning {
						recovery = append(recovery, control(previous.Name, "restart"))
					}
				}
				return errors.Join(fmt.Errorf("服务 %s 重启失败：%w", item.Name, err), restoreErr, errors.Join(recovery...))
			}
			fmt.Println("已重启：", item.Name)
		}
	}
	tx.Commit()
	return nil
}
