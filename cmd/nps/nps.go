package main

import (
	"bufio"
	"ehang.io/nps/bridge"
	"ehang.io/nps/lib/daemon"
	"ehang.io/nps/server"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/fatih/color"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"

	npsconf "ehang.io/nps/conf"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/install"
	"ehang.io/nps/lib/version"
	"ehang.io/nps/server/connection"
	"ehang.io/nps/server/tool"
	"ehang.io/nps/web/audit"
	"ehang.io/nps/web/controllers"
	"ehang.io/nps/web/routers"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/web"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"

	"github.com/kardianos/service"
)

var (
	level            string
	ver              = flag.Bool("version", false, "show current version")
	gen2fa           = flag.Bool("gen2fa", false, "generate a TOTP secret for Web administrator login")
	confPath         = flag.String("conf_path", "", "set current confPath")
	serverCmd        = flag.Bool("server", false, "NPS管理脚本")
	npsLogPath       = flag.String("log_path", "", "nps log path")
	ipRegisterMu     sync.Mutex
	ipRegisterServer *server.IPRegistrationServer
)

func main() {

	debug.SetMaxThreads(1000000)

	flag.Parse()
	// init log
	if *ver {
		common.PrintVersion()
		return
	}
	if *gen2fa {
		secret, err := crypt.GenerateTOTPSecret()
		if err != nil {
			fmt.Fprintln(os.Stderr, "generate TOTP secret failed:", err)
			os.Exit(1)
		}
		fmt.Println("TOTP secret:", secret)
		fmt.Println("Authenticator URI:", crypt.BuildTOTPURI("NPS", "admin", secret))
		fmt.Println("Add this line to conf/nps.conf and restart NPS:")
		fmt.Println("totp_secret=" + secret)
		return
	}

	var logPath string
	// *confPath why get null value ?
	for _, v := range os.Args[1:] {
		switch v {
		case "install", "start", "stop", "uninstall", "restart":
			continue
		}
		if strings.Contains(v, "-conf_path=") {
			common.ConfPath = strings.Replace(v, "-conf_path=", "", -1)
		}

		if strings.Contains(v, "-log_path=") {
			logPath = strings.Replace(v, "-log_path=", "", -1)
		}
	}
	// Parse the explicit configuration directory before entering the
	// management menu.  This keeps `nps -server -conf_path=...` consistent
	// with normal service commands and prevents menu actions from silently
	// operating on the default directory.
	if *serverCmd {
		printSlogan()
		inputCmd()
		return
	}

	// auto-generate default config files if not exist
	initConfig(filepath.Join(common.GetRunPath(), "conf"))

	if err := beego.LoadAppConfig("ini", filepath.Join(common.GetRunPath(), "conf", "nps.conf")); err != nil {
		log.Fatalln("load config file error", err.Error())
	}
	file.SetConnectionHistoryLimit(beego.AppConfig.DefaultInt("client_history_limit", 5))
	audit.Configure(beego.AppConfig.String("audit_log_path"))

	common.InitPProfFromFile()
	if level = beego.AppConfig.String("log_level"); level == "" {
		level = "7"
	}
	if logLevel, ok := parseConfiguredLogLevel(level); ok {
		level = strconv.Itoa(logLevel)
	} else {
		fmt.Fprintf(os.Stderr, "invalid log_level %q; using debug level 7\n", level)
		level = "7"
	}
	logs.Reset()
	logs.EnableFuncCallDepth(true)
	logs.SetLogFuncCallDepth(3)

	if logPath == "" {
		logPath = beego.AppConfig.String("log_path")
		if logPath == "" {
			logPath = common.GetLogPath()
		}
	}

	// init service
	options := make(service.KeyValue)
	svcConfig := &service.Config{
		Name:        "Nps",
		DisplayName: "nps内网穿透代理服务器",
		Description: "一款轻量级、功能强大的内网穿透代理服务器。支持tcp、udp流量转发，支持内网http代理、内网socks5代理，同时支持snappy压缩、站点保护、加密传输、多路复用、header修改等。支持web图形化管理，集成多用户模式。",
		Option:      options,
	}

	bridge.ServerTlsEnable = beego.AppConfig.DefaultBool("tls_enable", false)

	for _, v := range os.Args[1:] {
		switch v {
		case "install", "start", "stop", "uninstall", "restart":
			continue
		}
		svcConfig.Arguments = append(svcConfig.Arguments, v)
	}

	svcConfig.Arguments = append(svcConfig.Arguments, "service")
	if len(os.Args) > 1 && os.Args[1] == "service" {
		if err := setRotatingFileLogger(logPath, level); err != nil {
			fmt.Fprintln(os.Stderr, "configure rotating NPS log failed:", err)
			_ = logs.SetLogger(logs.AdapterConsole, `{"level":7,"color":false}`)
			logs.Error("Falling back to console logging because the configured log file is unavailable")
		}
	} else {
		_ = logs.SetLogger(logs.AdapterConsole, `{"level":`+level+`,"color":true}`)
	}
	if !common.IsWindows() {
		svcConfig.Dependencies = []string{
			"Requires=network.target",
			"After=network-online.target syslog.target"}
		svcConfig.Option["SystemdScript"] = install.SystemdScript
		svcConfig.Option["SysvScript"] = install.SysvScript
	}
	prg := &nps{}
	prg.exit = make(chan struct{})
	s, err := service.New(prg, svcConfig)
	if err != nil {
		logs.Error(err, "service function disabled")
		if runErr := run(); runErr != nil {
			logs.Error("NPS startup failed: ", runErr)
			return
		}
		// run without service
		wg := sync.WaitGroup{}
		wg.Add(1)
		wg.Wait()
		return
	}

	if len(os.Args) > 1 && os.Args[1] != "service" {
		switch os.Args[1] {
		case "reload":
			daemon.InitDaemon("nps", common.GetRunPath(), common.GetTmpPath())
			return
		case "install":
			// uninstall before
			_ = service.Control(s, "stop")
			_ = service.Control(s, "uninstall")

			binPath := install.InstallNps()
			svcConfig.Executable = binPath
			s, err := service.New(prg, svcConfig)
			if err != nil {
				logs.Error(err)
				return
			}
			err = service.Control(s, os.Args[1])
			if err != nil {
				logs.Error("Valid actions: %q\n%s", service.ControlAction, err.Error())
			}
			if service.Platform() == "unix-systemv" {
				logs.Info("unix-systemv service")
				confPath := "/etc/init.d/" + svcConfig.Name
				os.Symlink(confPath, "/etc/rc.d/S90"+svcConfig.Name)
				os.Symlink(confPath, "/etc/rc.d/K02"+svcConfig.Name)
			}
			return
		case "start", "restart", "stop":
			if service.Platform() == "unix-systemv" {
				logs.Info("unix-systemv service")
				cmd := exec.Command("/etc/init.d/"+svcConfig.Name, os.Args[1])
				err := cmd.Run()
				if err != nil {
					logs.Error(err)
				}
				return
			}
			err := service.Control(s, os.Args[1])
			if err != nil {
				logs.Error("Valid actions: %q\n%s", service.ControlAction, err.Error())
			}
			return
		case "uninstall":
			err := service.Control(s, os.Args[1])
			if err != nil {
				logs.Error("Valid actions: %q\n%s", service.ControlAction, err.Error())
			}
			if service.Platform() == "unix-systemv" {
				logs.Info("unix-systemv service")
				os.Remove("/etc/rc.d/S90" + svcConfig.Name)
				os.Remove("/etc/rc.d/K02" + svcConfig.Name)
			}
			return
		case "update":
			if updated, err := install.UpdateNpsResult(); err != nil {
				logs.Error("更新失败：", err)
			} else if updated {
				restartNpsService(s)
			}
			return
			//default:
			//	logs.Error("command is not support")
			//	return
		}
	}

	if err := s.Run(); err != nil {
		logs.Error("NPS service failed: ", err)
		os.Exit(1)
	}
}

func printSlogan() {
	green := color.New(color.FgGreen).SprintFunc()
	yellow := color.New(color.FgYellow).SprintFunc()

	_ = green

	fmt.Println()
	fmt.Printf("  %s\n", yellow("NPS 内网穿透服务端 v"+version.VERSION))
	fmt.Println()
	fmt.Println("  [1] 安装 NPS")
	fmt.Println("  [2] 删除/卸载 NPS")
	fmt.Println("  [3] 更新 NPS")
	fmt.Println("  [4] 查看状态")
	fmt.Println("  [5] 启动 NPS")
	fmt.Println("  [6] 停止 NPS")
	fmt.Println("  [7] 重启 NPS")
	fmt.Println("  [0] 退出")
	fmt.Println()
}

func inputCmd() {
	for {
		var input string
		fmt.Printf("请输入[0-7]：")

		stdin := bufio.NewReader(os.Stdin)
		_, err := fmt.Fscanln(stdin, &input)
		if err != nil {
			fmt.Println("输入有误，请重新输入")
			continue
		}

		if input == "0" {
			os.Exit(0)
		}

		prg := &nps{
			exit: make(chan struct{}),
		}
		options := make(service.KeyValue)
		svcConfig := &service.Config{
			Name:        "Nps",
			DisplayName: "nps内网穿透代理服务器",
			Description: "一款轻量级、功能强大的内网穿透代理服务器。支持tcp、udp流量转发，支持内网http代理、内网socks5代理，同时支持snappy压缩、站点保护、加密传输、多路复用、header修改等。支持web图形化管理，集成多用户模式。",
			Option:      options,
		}
		s, _ := service.New(prg, svcConfig)

		switch input {
		case "1":
			_ = service.Control(s, "stop")
			_ = service.Control(s, "uninstall")
			binPath := install.InstallNpsToCurrentDir()

			beego.LoadAppConfig("ini", filepath.Join(common.GetAppPath(), "conf", "nps.conf"))

			logPath := filepath.Join(common.GetAppPath(), "nps.log")
			if common.IsWindows() {
				logPath = strings.Replace(logPath, "\\", "\\\\", -1)
			}
			svcConfig.Arguments = append(svcConfig.Arguments, "service")
			svcConfig.Arguments = append(svcConfig.Arguments, "-conf_path="+common.GetAppPath())
			svcConfig.Arguments = append(svcConfig.Arguments, "-log_path="+logPath)

			fmt.Println("日志文件路径为：", logPath)

			svcConfig.Executable = binPath
			s, err := service.New(prg, svcConfig)

			if service.Platform() == "unix-systemv" {
				logs.Info("unix-systemv service")
				confPath := "/etc/init.d/" + svcConfig.Name
				os.Symlink(confPath, "/etc/rc.d/S90"+svcConfig.Name)
				os.Symlink(confPath, "/etc/rc.d/K02"+svcConfig.Name)
			}

			err = service.Control(s, "install")
			if err != nil {
				logs.Error("Valid actions: %q\n%s", service.ControlAction, err.Error())
			} else {
				fmt.Println("NPS服务安装成功")
			}

			err = service.Control(s, "start")
			if err != nil {
				fmt.Println("启动NPS服务失败", err)
			} else {
				fmt.Println("NPS服务已启动，管理面板访问地址：127.0.0.1:" + beego.AppConfig.String("web_port"))
			}

		case "2":
			err := service.Control(s, "stop")
			if err != nil {
				fmt.Println("NPS服务停止失败", err)
			} else {
				fmt.Println("NPS服务已停止")
			}

			err = service.Control(s, "uninstall")
			if err != nil {
				logs.Error("NPS服务卸载失败")
			}
			if service.Platform() == "unix-systemv" {
				logs.Info("unix-systemv service")
				os.Remove("/etc/rc.d/S90" + svcConfig.Name)
				os.Remove("/etc/rc.d/K02" + svcConfig.Name)
			}

			if err == nil {
				fmt.Println("NPS服务已卸载成功")
			}

		case "3":
			if updated, err := install.UpdateNpsResult(); err != nil {
				fmt.Println("更新失败：", err)
			} else if updated {
				restartNpsService(s)
			}
			break
		case "4":
			var statusMsg = ""
			status, err := s.Status()
			if err != nil {
				statusMsg = "未运行"
			} else {
				if status == 1 {
					statusMsg = "运行中"
				} else {
					statusMsg = "未运行"
				}
			}
			fmt.Println("NPS服务状态：" + statusMsg)

		case "5":
			err := service.Control(s, "start")
			if err != nil {
				fmt.Println("NPS服务启动失败", err)
			} else {
				fmt.Println("NPS服务启动成功")
			}

		case "6":
			err := service.Control(s, "stop")
			if err != nil {
				fmt.Println("NPS服务停止失败", err)
			} else {
				fmt.Println("NPS服务停止成功")
			}

		case "7":
			err := service.Control(s, "restart")
			if err != nil {
				fmt.Println("NPS服务重启失败", err)
			} else {
				fmt.Println("NPS服务重启成功")
			}
		}
	}
}

// restartNpsService restarts an already installed service after a successful
// binary update. A non-service foreground process is left untouched.
func restartNpsService(s service.Service) {
	if s == nil {
		fmt.Println("未检测到已安装的 NPS 服务，请手动重启")
		return
	}
	if _, err := s.Status(); err != nil {
		fmt.Println("未检测到已安装的 NPS 服务，请手动重启")
		return
	}
	if service.Platform() == "unix-systemv" {
		if err := exec.Command("/etc/init.d/"+"Nps", "restart").Run(); err != nil {
			fmt.Println("NPS 服务自动重启失败，请手动重启：", err)
			return
		}
	} else if err := service.Control(s, "restart"); err != nil {
		fmt.Println("NPS 服务自动重启失败，请手动重启：", err)
		return
	}
	fmt.Println("NPS 服务已自动重启，新版本已生效")
}

func installNps() {

}

type nps struct {
	exit     chan struct{}
	stopOnce sync.Once
}

func (p *nps) Start(s service.Service) error {
	_, _ = s.Status()
	// run performs all required listener binds before returning, so reporting
	// an error here lets the service manager mark a failed startup correctly.
	return run()
}
func (p *nps) Stop(s service.Service) error {
	_, _ = s.Status()
	closeIPRegistrationServer()
	p.stopOnce.Do(func() { close(p.exit) })
	if service.Interactive() {
		os.Exit(0)
	}
	return nil
}

func (p *nps) run() (runErr error) {
	defer func() {
		if err := recover(); err != nil {
			const size = 64 << 10
			buf := make([]byte, size)
			buf = buf[:runtime.Stack(buf, false)]
			logs.Warning("nps: panic serving %v: %v\n%s", err, string(buf))
			runErr = fmt.Errorf("nps startup panic: %v", err)
		}
	}()
	if err := run(); err != nil {
		return err
	}
	select {
	case <-p.exit:
		logs.Warning("stop...")
	}
	return nil
}

func run() error {
	routers.Init()
	task := &file.Tunnel{
		Mode: "webServer",
	}
	bridgePort, err := beego.AppConfig.Int("bridge_port")
	if err != nil {
		return fmt.Errorf("get bridge_port: %w", err)
	}

	logs.Info("日志路径：" + *npsLogPath)
	logs.Info("the config path is:" + common.GetRunPath())
	logs.Info("the version of server is %s ,allow client core version to be %s,tls enable is %t", version.VERSION, version.GetVersion(), bridge.ServerTlsEnable)
	connection.InitConnectionService()
	//crypt.InitTls(filepath.Join(common.GetRunPath(), "conf", "server.pem"), filepath.Join(common.GetRunPath(), "conf", "server.key"))
	crypt.InitTls()
	if fingerprint := crypt.GetCertFingerprint(); fingerprint != "" {
		logs.Info("TLS Bridge certificate SHA-256 fingerprint: %s", fingerprint)
	}
	tool.InitAllowPort()
	tool.StartSystemInfo()
	common.ReportInstall("nps")
	timeout, err := beego.AppConfig.Int("disconnect_timeout")
	if err != nil {
		timeout = 60
	}
	registrationServer, err := server.StartIPRegistrationServer(controllers.IPRegisterStandaloneHandler())
	if err != nil {
		return fmt.Errorf("start standalone IP registration page: %w", err)
	}
	if err := server.StartNewServer(bridgePort, task, beego.AppConfig.String("bridge_type"), timeout); err != nil {
		if registrationServer != nil {
			_ = registrationServer.Close()
		}
		return fmt.Errorf("start NPS server: %w", err)
	}
	ipRegisterMu.Lock()
	ipRegisterServer = registrationServer
	ipRegisterMu.Unlock()
	return nil
}

func closeIPRegistrationServer() {
	ipRegisterMu.Lock()
	registrationServer := ipRegisterServer
	ipRegisterServer = nil
	ipRegisterMu.Unlock()
	if registrationServer != nil {
		if err := registrationServer.Close(); err != nil {
			logs.Warning("stop IP registration listener: %v", err)
		}
	}
}

func setRotatingFileLogger(logPath, configuredLevel string) error {
	logLevel, ok := parseConfiguredLogLevel(configuredLevel)
	if !ok {
		return fmt.Errorf("invalid log_level %q: expected a number from 0 to 7 or a supported level name", configuredLevel)
	}
	config := rotatingLogConfig{
		Filename:    logPath,
		MaxSizeMB:   int64(beego.AppConfig.DefaultInt("log_max_size", 2)),
		MaxAgeDays:  beego.AppConfig.DefaultInt("log_max_days", 7),
		MaxBackups:  beego.AppConfig.DefaultInt("log_max_files", 10),
		Compress:    beego.AppConfig.DefaultBool("log_compress", false),
		RotateDaily: beego.AppConfig.DefaultBool("log_rotate_daily", true),
		Level:       logLevel,
		Perm:        0640,
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode rotating log config: %w", err)
	}
	return logs.SetLogger(rotatingLogAdapter, string(encoded))
}

// parseConfiguredLogLevel accepts the historical Beego numeric levels and
// common text aliases. The numeric range retains Beego's convention: 0 is
// least verbose and 7 is most verbose.
func parseConfiguredLogLevel(configured string) (int, bool) {
	value := strings.ToLower(strings.TrimSpace(configured))
	switch value {
	case "trace", "debug":
		return logs.LevelDebug, true
	case "info", "informational":
		return logs.LevelInformational, true
	case "notice":
		return logs.LevelNotice, true
	case "warn", "warning":
		return logs.LevelWarning, true
	case "error":
		return logs.LevelError, true
	case "fatal", "critical":
		return logs.LevelCritical, true
	case "panic", "alert":
		return logs.LevelAlert, true
	case "emergency":
		return logs.LevelEmergency, true
	case "off", "none":
		return -1, true
	}

	level, err := strconv.Atoi(value)
	if err != nil || level < logs.LevelEmergency || level > logs.LevelDebug {
		return 0, false
	}
	return level, true
}

func initConfig(confDir string) {
	if !common.FileExists(confDir) {
		if err := os.MkdirAll(confDir, 0750); err != nil {
			logs.Error("create config directory failed:", err)
			return
		}
	}
	confPath := filepath.Join(confDir, "nps.conf")
	if !common.FileExists(confPath) {
		content, secrets := rotateInsecureConfig(defaultNpsConf)
		f, err := os.OpenFile(confPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			logs.Error("create config file failed:", err)
			return
		}
		if _, err := f.WriteString(content); err != nil {
			_ = f.Close()
			logs.Error("write config file failed:", err)
			return
		}
		_ = f.Close()
		_ = os.Chmod(confPath, 0600)
		logs.Info("Auto-generated default config file:", confPath)
		logGeneratedSecrets(secrets)
	} else if changed, added, secrets, err := migrateConfigFile(confPath, defaultNpsConf); err != nil {
		logs.Error("migrate config file failed:", err)
	} else if changed {
		if len(added) > 0 {
			logs.Info("Added missing settings to config file:", strings.Join(added, ", "))
		}
		if len(secrets) > 0 {
			logs.Warn("rotated known default config secrets in:", confPath)
			logGeneratedSecrets(secrets)
		}
	} else {
		// The file was already current.  Keep startup quiet rather than
		// rewriting it on every restart.
	}
	web.ExtractWebFiles(common.GetRunPath())
}

// rotateInsecureConfig replaces only values that are known release-template
// defaults. Empty values remain untouched because they are a supported way to
// explicitly disable optional authentication in an existing deployment.
func rotateInsecureConfig(content string) (string, map[string]string) {
	secrets := make(map[string]string)
	for _, item := range []struct {
		key      string
		length   int
		defaults []string
	}{
		{key: "web_password", length: 8, defaults: []string{"123", "CHANGE_ME"}},
		{key: "auth_key", length: 8, defaults: []string{"123", "CHANGE_ME"}},
		{key: "auth_crypt_key", length: 16, defaults: []string{"213", "CHANGE_ME_16CHAR"}},
	} {
		for _, value := range item.defaults {
			if !configValueEquals(content, item.key, value) {
				continue
			}
			generated := crypt.GetRandomString(item.length)
			var changed bool
			content, changed = replaceConfigValue(content, item.key, value, generated)
			if changed {
				secrets[item.key] = generated
			}
			break
		}
	}
	// The historical public key was shared by every packaged installation.
	// Disable it in-place; users can opt in with an explicit value later.
	if configValueEquals(content, "public_vkey", "123") {
		content, _ = replaceConfigValue(content, "public_vkey", "123", "")
		secrets["public_vkey"] = "(disabled)"
	}
	return content, secrets
}

func configValueEquals(content, key, expected string) bool {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == key && strings.TrimSpace(parts[1]) == expected {
			return true
		}
	}
	return false
}

func replaceConfigValue(content, key, expected, replacement string) (string, bool) {
	lines := strings.SplitAfter(content, "\n")
	changed := false
	for i, line := range lines {
		body := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		parts := strings.SplitN(body, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) != key || strings.TrimSpace(parts[1]) != expected {
			continue
		}
		newline := line[len(body):]
		lines[i] = body[:strings.Index(body, "=")+1] + replacement + newline
		changed = true
	}
	return strings.Join(lines, ""), changed
}

func logGeneratedSecrets(secrets map[string]string) {
	if value, ok := secrets["web_password"]; ok {
		logs.Info("Web login username: admin, password:", value)
	}
	if value, ok := secrets["auth_key"]; ok {
		logs.Info("auth_key:", value)
	}
	if value, ok := secrets["auth_crypt_key"]; ok {
		logs.Info("auth_crypt_key:", value)
	}
	if value, ok := secrets["public_vkey"]; ok {
		logs.Info("public_vkey:", value)
	}
}

var defaultNpsConf = npsconf.DefaultNpsConf
