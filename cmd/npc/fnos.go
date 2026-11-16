//go:build !sdk

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"ehang.io/nps/client"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/config"
	"ehang.io/nps/lib/fnos"
	"ehang.io/nps/lib/version"
	"github.com/astaxie/beego/logs"
)

// fnOS is an explicit host-managed mode. The ordinary CLI/menu/service/GUI
// paths and the Bridge wire protocol remain unchanged.
func fnosMain(action, root, wizardPath string) error {
	if runtime.GOOS != "linux" {
		return errors.New("飞牛运行模式仅支持 Linux")
	}
	if !filepath.IsAbs(root) {
		return errors.New("飞牛配置必须使用绝对路径")
	}
	if action == "child" {
		return runFNOSChild(root)
	}
	if action == "run" {
		return runFNOS(root)
	}
	if !filepath.IsAbs(wizardPath) {
		return errors.New("飞牛原生向导必须使用绝对路径")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	release, err := lockFNOSConfig(filepath.Join(root, ".config.lock"))
	if err != nil {
		return errors.New("配置正在被修改，请稍后重试")
	}
	defer release()
	current, err := fnos.Read(root)
	if err != nil {
		return err // Never replace unreadable or malformed npc.conf with defaults.
	}
	updated := current
	template := ""
	if (action == "install" || action == "configure") && *fnosTemplate != "" {
		if !filepath.IsAbs(*fnosTemplate) {
			return errors.New("npc.conf 模板必须使用绝对路径")
		}
		data, err := os.ReadFile(*fnosTemplate)
		if err != nil || len(data) > 1<<20 {
			return errors.New("无法读取包内 npc.conf 完整模板")
		}
		template = string(data)
	}
	switch action {
	case "install":
		if len(current.Connections) == 0 {
			updated, err = fnos.Apply(current, os.LookupEnv, template)
		}
	case "configure":
		updated, err = fnos.Apply(current, os.LookupEnv, template)
	case "prepare":
		// Rebuild only the native form; do not replay old wizard credentials.
	case "wizard-install":
		return fnos.AtomicWrite(wizardPath, fnos.InstallWizard(), 0644)
	case "wizard-config":
		data, err := fnos.Wizard(fnos.NewConfig(), false)
		if err != nil {
			return err
		}
		return fnos.AtomicWrite(wizardPath, data, 0644)
	default:
		return errors.New("未知的飞牛操作")
	}
	if err != nil {
		return err
	}
	clearLogs := false
	if action == "configure" {
		switch value := os.Getenv(fnos.FieldPrefix("logs", current.Revision) + "action"); value {
		case "", "keep":
		case "clear":
			clearLogs = true
		default:
			return errors.New("日志操作无效")
		}
	}
	preview := "尚无运行日志。"
	if filepath.IsAbs(*logPath) {
		preview = fnos.Redact(fnos.LogTail(*logPath), fnos.Secrets(current))
	}
	if clearLogs {
		if !filepath.IsAbs(*logPath) {
			return errors.New("未提供运行日志路径，不能清理日志")
		}
		preview = "运行日志已清空；新的连接日志将继续记录。"
	}
	if err := fnos.SaveWithWizard(root, wizardPath, updated, preview); err != nil {
		return err
	}
	if clearLogs {
		if err := fnos.ClearLogs(*logPath); err != nil {
			return fmt.Errorf("连接设置已保存，但日志清理失败：%w", err)
		}
	}
	return nil
}

func readFNOSLocked(root string) (fnos.Config, error) {
	release, err := lockFNOSConfig(filepath.Join(root, ".config.lock"))
	if err != nil {
		return fnos.Config{}, errors.New("正在保存配置，稍后重新读取")
	}
	defer release()
	return fnos.Read(root)
}

func runFNOS(root string) error {
	release, err := lockFNOSConfig(filepath.Join(root, ".runtime.lock"))
	if err != nil {
		return errors.New("此飞牛配置目录已经有一个运行实例")
	}
	defer release()
	current, err := readFNOSLocked(root)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(*logPath) {
		return errors.New("飞牛日志必须使用绝对路径")
	}
	writer, err := fnos.OpenLog(*logPath)
	if err != nil {
		return err
	}
	defer writer.Close()
	writer.SetSecrets(current)
	logger := &fnosClientLogger{writer: writer}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	status := &fnosStatus{running: map[string]bool{}}
	logDir := filepath.Join(filepath.Dir(*logPath), "logs")
	stateRoot := filepath.Join(filepath.Dir(*logPath), "connections")
	manager := &fnos.Manager{
		Factory: func(entry fnos.Entry) fnos.Connection {
			return newFNOSProcess(root, entry, writer, logDir)
		},
		Event: func(entry fnos.Entry, message string) {
			(&fnosClientLogger{entry: entry, writer: writer}).Info("%s", message)
		},
		Status: func(entry fnos.Entry, running bool) {
			status.set(entry.ID, running)
		},
		Cleanup: func(entry fnos.Entry) {
			_ = fnos.RemoveLogs(fnos.ConnectionLogPath(logDir, entry.ID))
			_ = os.RemoveAll(filepath.Join(stateRoot, entry.ID))
			_ = os.Remove(filepath.Join(root, "connections", entry.ID))
		},
		ResetLog: func(entry fnos.Entry) {
			_ = fnos.ClearLogs(fnos.ConnectionLogPath(logDir, entry.ID))
		},
	}
	defer manager.Close()
	manager.Apply(ctx, current)
	if err := startFNOSSettings(ctx, root, logger, logDir, status.get, manager.Memory); err != nil {
		return err
	}
	logger.Info("飞牛原生多配置模式已启动，共 %d 份 npc.conf；远程自更新已禁用", len(current.Connections))
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastError := ""
	pendingRevision := ""
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			updated, err := readFNOSLocked(root)
			if err != nil {
				if message := err.Error(); message != lastError {
					logger.Error("配置读取失败，继续使用原连接：%s", message)
					lastError = message
				}
				continue
			}
			lastError = ""
			if updated.Revision != current.Revision {
				// Wait for two matching valid reads. Editors which truncate then
				// rewrite a file should not cause needless reconnects mid-save.
				if pendingRevision != updated.Revision {
					pendingRevision = updated.Revision
					continue
				}
				writer.SetSecrets(updated)
				manager.Apply(ctx, updated)
				current = updated
				pendingRevision = ""
				logger.Info("已应用 npc.conf 变更，共 %d 份；未修改的连接保持运行", len(current.Connections))
			}
		}
	}
}

func startFNOSSettings(ctx context.Context, root string, logger *fnosClientLogger, logDir string, status func(string) (bool, bool), memory func(string) int64) error {
	if *fnosSocket == "" {
		return nil
	}
	if !filepath.IsAbs(*fnosSocket) {
		return errors.New("飞牛内嵌设置必须使用绝对 socket 路径")
	}
	if info, err := os.Lstat(*fnosSocket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("飞牛内嵌设置 socket 路径已被非 socket 文件占用")
		}
		if err := os.Remove(*fnosSocket); err != nil {
			return fmt.Errorf("无法清理旧的飞牛内嵌设置 socket：%w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", *fnosSocket)
	if err != nil {
		return fmt.Errorf("无法监听飞牛内嵌设置 socket：%w", err)
	}
	handler := &fnos.Server{
		Root: root, LogPath: *logPath, LogDir: logDir, TemplatePath: *fnosTemplate,
		Prefix: strings.TrimSuffix(*fnosGateway, "/"), Status: status, Memory: memory,
		AppVersion: version.VERSION, CoreVersion: version.GetVersion(),
	}
	server := &http.Server{Handler: handler}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		_ = os.Remove(*fnosSocket)
	}()
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("飞牛内嵌设置服务异常退出：%s", err)
		}
	}()
	return nil
}

func fnosUpdateCapability() common.UpdateCapability {
	return common.UpdateCapability{Protocol: 2, Kind: "cli", Mode: "fnos", Enabled: false, Reason: "飞牛客户端由应用中心管理，请安装新版 FPK；不允许远程自更新"}
}

// Each native configuration has its own process: the legacy file-mode local
// listener/DNS globals cannot let one VKey stop another VKey's listeners.
func runFNOSChild(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.New("无法读取 npc.conf")
	}
	entry, err := fnos.ParseEntry(fnos.MainID, string(data))
	if err != nil {
		return err
	}
	if !filepath.IsAbs(*fnosState) {
		return errors.New("飞牛连接状态目录必须使用绝对路径")
	}
	if err := os.MkdirAll(*fnosState, 0700); err != nil {
		return err
	}
	common.RuntimeTempPath = *fnosState
	logs.Reset()
	if err := logs.SetLogger(logs.AdapterConsole, `{"level":6,"color":false}`); err != nil {
		return err
	}
	logs.SetPrefix(fnosConnectionLabel(entry))
	defer logs.GetBeeLogger().Close()
	client.ConfigureRemoteUpdater(fnosUpdateCapability(), nil)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	kicked := atomic.Bool{}
	finished := make(chan struct{})
	var direct *client.TRPClient
	if entry.Sync {
		logs.Info("[配置] 检测到规则或 fnos_config_sync=true，使用原生配置文件模式；服务端需允许配置连接")
		go func() {
			defer close(finished)
			client.StartFromFile(path, func(string) { kicked.Store(true) })
		}()
	} else {
		cnf, err := config.NewConfig(path)
		if err != nil || cnf.CommonConfig == nil {
			return errors.New("npc.conf 加载失败，请检查配置内容")
		}
		c := cnf.CommonConfig
		if err := common.SetCustomDNS(c.DnsServer); err != nil {
			return errors.New("npc.conf DNS 配置无效")
		}
		if c.NtpServer != "" {
			common.ConfigureTimeSync(c.NtpServer, time.Duration(max(c.NtpInterval, 1))*time.Minute)
		}
		options := client.TLSOptionsFromConfig(c)
		direct = client.NewRPClientWithTLS(entry.Command.Server, entry.Command.VKey, entry.Command.Transport, c.ProxyUrl, cnf, c.DisconnectTime, entry.Command.TLS, options)
		defer direct.Close()
		go func() {
			defer close(finished)
			direct.Start()
			kicked.Store(direct.Kicked())
		}()
	}
	select {
	case <-ctx.Done():
		client.CloseLocalServer()
		return nil
	case <-finished:
		if kicked.Load() {
			logs.Info("[通知] 服务端已删除本条客户端，其他 VKey 不受影响")
			logs.GetBeeLogger().Close()
			os.Exit(42)
		}
		logs.Info("[配置] 本条运行已结束（可能关闭了 auto_reconnection），等待设置变更")
		<-ctx.Done()
		return nil
	}
}

type fnosProcess struct {
	entry  fnos.Entry
	root   string
	log    *fnos.LogWriter
	writer io.Writer
	ctx    context.Context
	cancel context.CancelFunc
	kicked atomic.Bool
	pid    atomic.Int64
}

type fnosStatus struct {
	mu      sync.Mutex
	running map[string]bool
}

func (s *fnosStatus) set(id string, running bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running[id] = running
}

func (s *fnosStatus) get(id string) (bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	running, ok := s.running[id]
	return running, ok
}

func newFNOSProcess(root string, entry fnos.Entry, fallback io.Writer, logDir string) *fnosProcess {
	ctx, cancel := context.WithCancel(context.Background())
	p := &fnosProcess{root: root, entry: entry, writer: fallback, ctx: ctx, cancel: cancel}
	if logDir != "" {
		if log, err := fnos.OpenLog(fnos.ConnectionLogPath(logDir, entry.ID)); err == nil {
			log.SetSecrets(fnos.Config{Connections: []fnos.Entry{entry}})
			p.log = log
			p.writer = log
		}
	}
	return p
}

func (p *fnosProcess) Start() {
	logger := &fnosClientLogger{entry: p.entry, writer: p.writer}
	executable, err := os.Executable()
	if err != nil {
		logger.Error("无法定位 NPC 程序")
		return
	}
	path := filepath.Join(p.root, fnos.RelativePath(p.entry.ID))
	cmd := exec.CommandContext(p.ctx, executable, "-fnos=child", "-fnos-config="+path,
		"-fnos-state="+filepath.Join(filepath.Dir(*logPath), "connections", p.entry.ID, "tmp"))
	cmd.Dir = filepath.Dir(path)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "wizard_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	configureFNOSProcess(cmd)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second
	reader, writer := io.Pipe()
	cmd.Stdout, cmd.Stderr = writer, writer
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		defer reader.Close()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), 64<<10)
		for scanner.Scan() {
			// The child already emits Beego's timestamped, source-tagged line
			// and its own connection prefix. Pass it through unchanged so the
			// runtime log stays a single clean line per event instead of being
			// wrapped with a second timestamp.
			_, _ = io.WriteString(p.writer, scanner.Text()+"\n")
		}
		if scanner.Err() != nil {
			logger.Error("子进程日志行过长，停止本条连接以避免日志阻塞")
			p.cancel()
		}
	}()
	err = cmd.Start()
	if err == nil {
		p.pid.Store(int64(cmd.Process.Pid))
		err = cmd.Wait()
	} else {
		logger.Error("无法启动 NPC 子进程（未输出命令行或密钥）")
	}
	p.pid.Store(0)
	_ = writer.Close()
	<-scanned
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 42 {
		p.kicked.Store(true)
	} else if err != nil && p.ctx.Err() == nil {
		logger.Error("NPC 子进程退出，将自动重试（未输出命令行或密钥）")
	}
}

func (p *fnosProcess) Memory() int64 {
	pid := p.pid.Load()
	if pid <= 0 {
		return 0
	}
	return processRSS(pid)
}

func processRSS(pid int64) int64 {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}

func (p *fnosProcess) Close() {
	p.cancel()
	if p.log != nil {
		_ = p.log.Close()
		p.log = nil
	}
}
func (p *fnosProcess) Kicked() bool { return p.kicked.Load() }

func fnosConnectionLabel(entry fnos.Entry) string {
	name := strings.TrimSpace(entry.Command.Name)
	if name == "" {
		name = entry.ID
		if name == fnos.MainID {
			name = "main"
		} else if len(name) > 6 {
			name = name[:6]
		}
	}
	return "[连接 " + name + " · " + entry.Command.ProtocolName() + "]"
}

type fnosClientLogger struct {
	entry  fnos.Entry
	writer io.Writer
}

func (l *fnosClientLogger) message(format string, args ...interface{}) string {
	message := fnos.Redact(fmt.Sprintf(format, args...), fnos.Secrets(fnos.Config{Connections: []fnos.Entry{l.entry}}))
	message = strings.NewReplacer("\r", " ", "\n", " ", "\x00", "").Replace(message)
	if len(message) > 8192 {
		message = message[:8192] + "…"
	}
	if l.entry.ID != "" {
		return fnosConnectionLabel(l.entry) + " " + message
	}
	return "[飞牛] " + message
}

func (l *fnosClientLogger) write(level, format string, args ...interface{}) {
	message := l.message(format, args...)
	source := ""
	if _, file, line, ok := runtime.Caller(2); ok {
		_, filename := path.Split(file)
		source = "[" + filename + ":" + strconv.Itoa(line) + "] "
	}
	_, _ = fmt.Fprintf(l.writer, "%s %s %s%s\n", time.Now().Format("2006/01/02 15:04:05.000"), level, source, message)
}
func (l *fnosClientLogger) Info(format string, args ...interface{})  { l.write("[I]", format, args...) }
func (l *fnosClientLogger) Error(format string, args ...interface{}) { l.write("[E]", format, args...) }
