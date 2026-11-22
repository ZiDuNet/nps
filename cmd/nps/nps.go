package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/bridge"
	npsconf "ehang.io/nps/conf"
	"ehang.io/nps/lib/connectionlog"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/daemon"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/install"
	"ehang.io/nps/lib/settings"
	"ehang.io/nps/lib/store"
	_ "ehang.io/nps/lib/store/sqlitedriver"
	"ehang.io/nps/lib/version"
	"ehang.io/nps/server"
	"ehang.io/nps/server/connection"
	"ehang.io/nps/server/tool"
	"ehang.io/nps/server/usage"
	"ehang.io/nps/web/audit"
	"ehang.io/nps/web/controllers"
	"ehang.io/nps/web/routers"

	"ehang.io/nps/lib/acl"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/web"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"
	"github.com/fatih/color"
	"github.com/kardianos/service"
)

var (
	level            string
	ver              = flag.Bool("version", false, "show current version")
	gen2fa           = flag.Bool("gen2fa", false, "generate a TOTP secret for Web administrator login")
	confPath         = flag.String("conf_path", "", "set current confPath")
	serverCmd        = flag.Bool("server", false, "NPS管理脚本")
	npsLogPath       = flag.String("log_path", "", "nps log path")
	jsonGateway      = flag.Bool("json-gateway", false, "normalize legacy JSON data once and exit")
	exportJSON       = flag.String("export-json", "", "export the SQLite database to a JSON directory and exit")
	ipRegisterMu     sync.Mutex
	ipRegisterServer *server.IPRegistrationServer
	accessListMu     sync.Mutex
	accessListCancel context.CancelFunc
)

func main() {

	debug.SetMaxThreads(1000000)

	args := os.Args[1:]
	command := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	_ = flag.CommandLine.Parse(args)
	if command == "" && flag.NArg() == 1 {
		command = flag.Arg(0)
	} else if flag.NArg() != 0 {
		log.Fatalln("unexpected NPS arguments")
	}
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
		fmt.Println("This command only generates a secret. To enable TOTP, use Access and security in the Web panel.")
		return
	}

	if strings.TrimSpace(*confPath) != "" {
		absolute, err := filepath.Abs(strings.TrimSpace(*confPath))
		if err != nil {
			log.Fatalln("resolve configuration directory:", err)
		}
		common.ConfPath = absolute
	}
	// Data maintenance commands must observe -conf_path before they touch the
	// filesystem. Keeping these after the argument scan also covers the
	// separated "--conf_path <dir>" form.
	if *jsonGateway {
		result, err := file.RunJSONGateway(common.GetRunPath())
		if err != nil {
			fmt.Fprintln(os.Stderr, "JSON gateway failed:", err)
			os.Exit(1)
		}
		fmt.Printf("JSON gateway complete: clients=%d users=%d tunnels=%d hosts=%d marker=%s\n",
			result.Clients, result.Users, result.Tunnels, result.Hosts, result.MarkerPath)
		return
	}
	if *exportJSON != "" {
		if err := file.ExportSQLiteToJSON(common.GetRunPath(), *exportJSON); err != nil {
			fmt.Fprintln(os.Stderr, "export JSON failed:", err)
			os.Exit(1)
		}
		fmt.Println("JSON export complete:", *exportJSON)
		return
	}
	// Parse the explicit configuration directory before entering the
	// management menu.  This keeps `nps -server -conf_path=...` consistent
	// with normal service commands and prevents menu actions from silently
	// operating on the default directory.
	if *serverCmd {
		printSlogan()
		inputCmd(os.Stdin)
		return
	}

	if command != "" && command != "service" {
		if err := manageNpsCommand(command); err != nil {
			log.Fatalln("NPS service:", err)
		}
		return
	}
	if err := configureNpsStartup(common.GetRunPath(), command == "service"); err != nil {
		log.Fatalln("NPS startup failed:", err)
	}
	prg := &nps{exit: make(chan struct{})}
	s, err := service.New(prg, newNpsServiceConfig())
	if err != nil {
		logs.Error(err, "service function disabled")
		if runErr := run(); runErr != nil {
			log.Fatalln("NPS startup failed:", runErr)
		}
		<-prg.exit
		return
	}
	if err := s.Run(); err != nil {
		log.Fatalln("NPS service failed:", err)
	}
}

func configureNpsStartup(runPath string, serviceMode bool) error {
	if err := file.InitStorage(runPath, npsStartupConfig(runPath)); err != nil {
		return fmt.Errorf("initialize data storage: %w", err)
	}
	web.ExtractWebFiles(runPath)
	file.SetConnectionHistoryLimit(beego.AppConfig.DefaultInt("client_history_limit", 5))
	file.SetFlowLimitType(beego.AppConfig.String("flow_limit_type"))
	audit.Configure(beego.AppConfig.String("audit_log_path"))
	connectionlog.Configure(beego.AppConfig.String("connection_log_path"))
	if timezone := strings.TrimSpace(beego.AppConfig.String("timezone")); timezone != "" {
		if err := common.SetTimezone(timezone); err != nil {
			fmt.Fprintf(os.Stderr, "invalid timezone %q: %v\n", timezone, err)
		}
	}
	if ntpServer := strings.TrimSpace(beego.AppConfig.String("ntp_server")); ntpServer != "" {
		interval := time.Duration(beego.AppConfig.DefaultInt("ntp_interval", 5)) * time.Minute
		common.ConfigureTimeSync(ntpServer, interval)
	}
	if dnsServer := strings.TrimSpace(beego.AppConfig.String("dns_server")); dnsServer != "" {
		if err := common.SetCustomDNS(dnsServer); err != nil {
			fmt.Fprintf(os.Stderr, "invalid dns_server %q: %v\n", dnsServer, err)
		}
	}
	geoIPPath := strings.TrimSpace(beego.AppConfig.String("geoip_path"))
	geoSitePath := strings.TrimSpace(beego.AppConfig.String("geosite_path"))
	// GEO 数据默认放在运行目录的 conf 下；空配置按上游默认路径处理，
	// 保证“在线更新 GEO 数据”功能开箱即用，只有文件存在后才会参与匹配。
	if geoIPPath == "" {
		geoIPPath = filepath.Join(common.GetRunPath(), "conf", "geoip.dat")
	} else if !filepath.IsAbs(geoIPPath) {
		geoIPPath = filepath.Join(common.GetRunPath(), geoIPPath)
	}
	if geoSitePath == "" {
		geoSitePath = filepath.Join(common.GetRunPath(), "conf", "geosite.dat")
	} else if !filepath.IsAbs(geoSitePath) {
		geoSitePath = filepath.Join(common.GetRunPath(), geoSitePath)
	}
	acl.SetGeoData(geoIPPath, geoSitePath)
	install.SetGeoDataPaths(geoIPPath, geoSitePath)
	acl.SetMaxRules(beego.AppConfig.DefaultInt("acl_max_rules", acl.DefaultMaxRules))

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

	logPath := strings.TrimSpace(*npsLogPath)
	if logPath == "" {
		logPath = beego.AppConfig.String("log_path")
		if logPath == "" {
			logPath = common.GetLogPath()
		}
	}

	bridge.ServerTlsEnable = beego.AppConfig.DefaultBool("tls_enable", false)

	if serviceMode {
		if err := setRotatingFileLogger(logPath, level); err != nil {
			fmt.Fprintln(os.Stderr, "configure rotating NPS log failed:", err)
			_ = logs.SetLogger(logs.AdapterConsole, `{"level":7,"color":false}`)
			logs.Error("Falling back to console logging because the configured log file is unavailable")
		}
	} else {
		_ = logs.SetLogger(logs.AdapterConsole, `{"level":`+level+`,"color":true}`)
	}
	return nil
}

func npsStartupConfig(runPath string) file.StartupConfig {
	return file.StartupConfig{
		Defaults:        defaultNpsConf,
		InitialSettings: initialNpsSettings,
		Apply: func(original settings.StartupINI, values map[string]string, _ bool) error {
			return prepareStartupConfig(runPath, original, values)
		},
	}
}

func initialNpsSettings(ini settings.StartupINI) (map[string]string, error) {
	if !npsconf.IsBootstrapConfig(ini.Content) {
		return ini.Values, nil
	}
	// The compact starter (embedded or copied from config-examples) omits
	// business settings. Seed them once, without overriding explicit values.
	defaults, err := settings.ParseStartupConfig(npsconf.ServerConfigDefaults)
	if err != nil {
		return nil, err
	}
	maps.Copy(defaults.Values, ini.Values)
	return defaults.Values, nil
}

func newNpsServiceConfig() *service.Config {
	svcConfig := &service.Config{
		Name:        "Nps",
		DisplayName: "nps内网穿透代理服务器",
		Description: "一款轻量级、功能强大的内网穿透代理服务器。支持tcp、udp流量转发，支持内网http代理、内网socks5代理，同时支持snappy压缩、站点保护、加密传输、多路复用、header修改等。支持web图形化管理，集成多用户模式。",
		Option:      make(service.KeyValue),
	}
	if !common.IsWindows() {
		svcConfig.Dependencies = []string{
			"Requires=network.target",
			"After=network-online.target syslog.target"}
		svcConfig.Option["SystemdScript"] = install.SystemdScript
		svcConfig.Option["SysvScript"] = install.SysvScript
	}
	return svcConfig
}

func manageNpsCommand(command string) error {
	if command == "reload" {
		daemon.InitDaemon("nps", common.GetRunPath(), common.GetTmpPath())
		return nil
	}
	prg := &nps{exit: make(chan struct{})}
	svcConfig := newNpsServiceConfig()
	s, err := service.New(prg, svcConfig)
	if err != nil {
		return err
	}

	switch command {
	case "install":
		_ = service.Control(s, "stop")
		_ = service.Control(s, "uninstall")
		svcConfig.Executable = install.InstallNps()
		svcConfig.Arguments = serviceArguments(common.GetInstallPath(), *npsLogPath)
		s, err = service.New(prg, svcConfig)
		if err != nil {
			return err
		}
		if err := service.Control(s, "install"); err != nil {
			return err
		}
		if service.Platform() == "unix-systemv" {
			path := "/etc/init.d/" + svcConfig.Name
			_ = os.Symlink(path, "/etc/rc.d/S90"+svcConfig.Name)
			_ = os.Symlink(path, "/etc/rc.d/K02"+svcConfig.Name)
		}
	case "start", "restart", "stop":
		if service.Platform() == "unix-systemv" {
			return exec.Command("/etc/init.d/"+svcConfig.Name, command).Run()
		}
		return service.Control(s, command)
	case "uninstall":
		if err := service.Control(s, command); err != nil {
			return err
		}
		if service.Platform() == "unix-systemv" {
			_ = os.Remove("/etc/rc.d/S90" + svcConfig.Name)
			_ = os.Remove("/etc/rc.d/K02" + svcConfig.Name)
		}
	case "update":
		updated, err := updateInstalledNps(s, common.GetRunPath())
		if err != nil {
			return fmt.Errorf("更新失败: %w", err)
		}
		if updated {
			fmt.Println("NPS 更新完成并通过健康检查")
		}
	case "status":
		printServiceStatus(s)
	default:
		return fmt.Errorf("unsupported NPS command: %s", command)
	}
	return nil
}

func serviceArguments(runPath, logPath string) []string {
	args := []string{"service", "-conf_path=" + runPath}
	if logPath != "" {
		args = append(args, "-log_path="+logPath)
	}
	return args
}

func printServiceStatus(s service.Service) {
	status, err := s.Status()
	if err == nil && status == service.StatusRunning {
		fmt.Println("NPS服务状态：运行中")
	} else {
		fmt.Println("NPS服务状态：未运行")
	}
}

// updateInstalledNps resolves the service registration before touching the
// database or replacing any file. The menu is often launched from ~, while
// the running service can use a completely different binary and conf path.
func updateInstalledNps(s service.Service, runPath string) (bool, error) {
	if s == nil {
		return false, errors.New("未检测到已安装的 NPS 服务")
	}
	if _, err := s.Status(); err != nil {
		return false, errors.New("未检测到已安装的 NPS 服务，请先安装服务")
	}
	svcConfig := newNpsServiceConfig()
	target, err := install.RegisteredNpsUpdateTarget(svcConfig.Name)
	if err != nil {
		return false, err
	}
	command, err := install.RegisteredServiceCommand(svcConfig.Name)
	if err != nil {
		return false, fmt.Errorf("读取 NPS 服务运行参数: %w", err)
	}
	if strings.TrimSpace(command.ConfigPath) != "" {
		runPath, err = filepath.Abs(strings.TrimSpace(command.ConfigPath))
		if err != nil {
			return false, fmt.Errorf("解析 NPS 服务配置目录: %w", err)
		}
	} else {
		// The menu executable may live in a download or temporary directory.
		// Without an explicit -conf_path, the service resolves conf next to its
		// registered executable, so that is the runtime path health must read.
		runPath = filepath.Dir(target)
	}
	staged, err := install.StageNpsUpdate(target)
	if err != nil {
		return false, err
	}
	if !staged.Downloaded {
		fmt.Println("当前已是最新版本，无需更新")
		return false, nil
	}
	health := func(runPath string, timeout time.Duration) error {
		return verifyUpdatedNpsHealth(runPath, command.Environment, timeout)
	}
	return true, installNpsUpdateAndVerifyWith(s, runPath, staged, health)
}

type npsServiceController interface {
	Status() (service.Status, error)
	Stop() error
	Start() error
}

type updateHealthChecker func(runPath string, timeout time.Duration) error

func installNpsUpdateAndVerify(s npsServiceController, runPath string, staged *install.StagedNpsUpdate) error {
	return installNpsUpdateAndVerifyWith(s, runPath, staged, func(runPath string, timeout time.Duration) error {
		return verifyUpdatedNpsHealth(runPath, nil, timeout)
	})
}

func installNpsUpdateAndVerifyWith(s npsServiceController, runPath string, staged *install.StagedNpsUpdate, check updateHealthChecker) error {
	if s == nil || staged == nil || !staged.Downloaded {
		return errors.New("更新状态无效")
	}
	if err := s.Stop(); err != nil {
		staged.DiscardDownload()
		return fmt.Errorf("停止旧 NPS 服务: %w", err)
	}
	if err := staged.Install(); err != nil {
		startErr := s.Start()
		staged.DiscardDownload()
		if startErr != nil {
			return fmt.Errorf("替换新程序失败: %v；恢复旧服务失败: %w", err, startErr)
		}
		return fmt.Errorf("替换新程序失败，已恢复旧服务: %w", err)
	}
	startErr := s.Start()
	if startErr == nil {
		startErr = check(runPath, 30*time.Second)
	}
	if startErr == nil {
		staged.Commit()
		return nil
	}
	stopErr := s.Stop()
	rollbackErr := staged.Rollback()
	restartErr := s.Start()
	if rollbackErr == nil {
		staged.Commit()
	}
	return errors.Join(
		fmt.Errorf("新版本启动或健康检查失败: %w", startErr),
		wrapIfErr("停止失败的新服务", stopErr),
		wrapIfErr("恢复旧程序", rollbackErr),
		wrapIfErr("启动旧服务", restartErr),
	)
}

func wrapIfErr(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// verifyUpdatedNpsHealth proves that the newly started service owns its
// configured SQLite lock and answers the public version endpoint. ProbeLock
// is non-mutating and detects a process that exited before acquiring the lock.
func verifyUpdatedNpsHealth(runPath string, serviceEnvironment []string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		locked, err := store.ProbeLock(filepath.Join(runPath, "conf", "nps.db"))
		if err == nil && locked {
			if err := verifyNpsPanelHealth(runPath, serviceEnvironment); err == nil {
				return nil
			} else {
				lastErr = err
			}
		} else if err != nil {
			lastErr = err
		} else {
			lastErr = errors.New("新服务尚未取得 SQLite 数据库锁")
		}
		time.Sleep(time.Second)
	}
	if lastErr == nil {
		lastErr = errors.New("等待新服务启动超时")
	}
	return lastErr
}

var npsPanelHealthRequest = func(scheme, address, basePath string) (*http.Response, error) {
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // local self-signed panel certificate
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return client.Get(scheme + "://" + address + basePath + "/api/v1/")
}

func verifyNpsPanelHealth(runPath string, serviceEnvironment []string) error {
	ini, err := settings.ReadStartupConfig(filepath.Join(runPath, "conf", "nps.conf"), defaultNpsConf)
	if err != nil {
		return fmt.Errorf("读取新服务启动配置: %w", err)
	}
	values := ini.Values
	port := strings.TrimSpace(values["web_port"])
	if port == "" || port == "0" {
		return nil
	}
	ip := strings.TrimSpace(serviceEnvironmentValue(serviceEnvironment, "NPS_WEB_IP"))
	if ip == "" {
		ip = strings.TrimSpace(values["web_ip"])
	}
	switch ip {
	case "", "0.0.0.0":
		ip = "127.0.0.1"
	case "::", "[::]":
		ip = "::1"
	}
	if ip == "" {
		ip = "127.0.0.1"
	}
	scheme := "http"
	if strings.EqualFold(strings.TrimSpace(values["web_open_ssl"]), "true") {
		scheme = "https"
	}
	// NPS_WEB_BASE_URL is not read by the server runtime; only the panel
	// config controls route and static-asset mounting. Keeping the same source
	// here prevents a stale service override from making a healthy panel look
	// unhealthy.
	basePath := strings.TrimSpace(values["web_base_url"])
	if basePath != "" && !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	basePath = strings.TrimRight(basePath, "/")
	response, err := npsPanelHealthRequest(scheme, net.JoinHostPort(ip, port), basePath)
	if err != nil {
		return fmt.Errorf("请求新服务健康端点: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("新服务健康端点返回 HTTP %d", response.StatusCode)
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("解析新服务健康响应: %w", err)
	}
	if payload.Version != version.VERSION {
		return fmt.Errorf("新服务版本为 %q，期望 %q", payload.Version, version.VERSION)
	}
	return nil
}

func serviceEnvironmentValue(environment []string, key string) string {
	prefix := key + "="
	for index := len(environment) - 1; index >= 0; index-- {
		if strings.HasPrefix(environment[index], prefix) {
			return strings.TrimPrefix(environment[index], prefix)
		}
	}
	return ""
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

func inputCmd(inputReader io.Reader) {
	stdin := bufio.NewScanner(inputReader)
	for {
		fmt.Printf("请输入[0-7]：")
		if !stdin.Scan() {
			if err := stdin.Err(); err != nil {
				fmt.Fprintln(os.Stderr, "读取菜单输入失败：", err)
			}
			return
		}
		input := strings.TrimSpace(stdin.Text())
		if input == "0" {
			return
		}

		prg := &nps{exit: make(chan struct{})}
		svcConfig := newNpsServiceConfig()
		s, err := service.New(prg, svcConfig)
		if err != nil {
			fmt.Fprintln(os.Stderr, "无法使用系统服务：", err)
			continue
		}

		switch input {
		case "1":
			_ = service.Control(s, "stop")
			_ = service.Control(s, "uninstall")
			binPath := install.InstallNpsToCurrentDir()

			runPath := common.GetRunPath()
			logPath := strings.TrimSpace(*npsLogPath)
			if logPath == "" {
				logPath = filepath.Join(runPath, "nps.log")
			}
			svcConfig.Arguments = serviceArguments(runPath, logPath)

			fmt.Println("日志文件路径为：", logPath)

			svcConfig.Executable = binPath
			s, err := service.New(prg, svcConfig)
			if err != nil {
				fmt.Println("创建 NPS 服务失败：", err)
				continue
			}

			if err := service.Control(s, "install"); err != nil {
				logs.Error("Valid actions: %q\n%s", service.ControlAction, err.Error())
				continue
			}
			fmt.Println("NPS服务安装成功")
			if service.Platform() == "unix-systemv" {
				path := "/etc/init.d/" + svcConfig.Name
				_ = os.Symlink(path, "/etc/rc.d/S90"+svcConfig.Name)
				_ = os.Symlink(path, "/etc/rc.d/K02"+svcConfig.Name)
			}
			if err := service.Control(s, "start"); err != nil {
				fmt.Println("启动NPS服务失败", err)
			} else {
				fmt.Println("NPS 服务启动请求已提交，请查看日志确认管理面板地址和启动结果")
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
			if updated, err := updateInstalledNps(s, common.GetRunPath()); err != nil {
				fmt.Println("更新失败：", err)
			} else if updated {
				fmt.Println("NPS 更新完成并通过健康检查")
			}
		case "4":
			printServiceStatus(s)

		case "5":
			err := service.Control(s, "start")
			if err != nil {
				fmt.Println("NPS服务启动失败", err)
			} else {
				fmt.Println("NPS 服务启动请求已提交，请查看日志确认启动结果")
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
		default:
			fmt.Println("输入有误，请重新输入")
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
	usage.Stop()
	closeIPRegistrationServer()
	stopAccessListWatcher()
	p.stopOnce.Do(func() { close(p.exit) })
	if db := file.GetDb(); db != nil && db.JsonDb != nil {
		if err := db.JsonDb.Flush(); err != nil {
			logs.Error("flush final data snapshot failed:", err)
		}
	}
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
	controllers.StartVersionCheckWorker()
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
	if err := connection.InitConnectionService(); err != nil {
		return fmt.Errorf("initialize connection listeners: %w", err)
	}
	bridgeCert := strings.TrimSpace(beego.AppConfig.String("bridge_cert_file"))
	bridgeKey := strings.TrimSpace(beego.AppConfig.String("bridge_key_file"))
	if bridgeCert != "" && !filepath.IsAbs(bridgeCert) {
		bridgeCert = filepath.Join(common.GetRunPath(), bridgeCert)
	}
	if bridgeKey != "" && !filepath.IsAbs(bridgeKey) {
		bridgeKey = filepath.Join(common.GetRunPath(), bridgeKey)
	}
	if bridgeCert != "" || bridgeKey != "" {
		if err := crypt.InitTlsFromFiles(bridgeCert, bridgeKey); err != nil {
			logs.Warn("配置的 Bridge 证书加载失败，将生成临时证书：%v", err)
			crypt.InitTls()
		} else {
			logs.Info("已加载配置的 Bridge TLS 证书")
		}
	} else {
		crypt.InitTls()
	}
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
		stopAccessListWatcher()
		if registrationServer != nil {
			_ = registrationServer.Close()
		}
		return fmt.Errorf("start NPS server: %w", err)
	}
	ipRegisterMu.Lock()
	ipRegisterServer = registrationServer
	ipRegisterMu.Unlock()
	usage.Start()
	startAccessListWatcher()
	return nil
}

// startAccessListWatcher keeps GeoIP/Geosite-backed ACLs in sync with files
// updated by an external downloader. Empty nps.conf paths resolve to the
// default data files, so those deployments need the watcher as well. The
// watcher is idempotent across service reload paths.
func startAccessListWatcher() {
	ipPath, sitePath := acl.GeoFiles()
	if ipPath == "" && sitePath == "" {
		return
	}
	accessListMu.Lock()
	defer accessListMu.Unlock()
	if accessListCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	accessListCancel = cancel
	interval := time.Minute
	if seconds := beego.AppConfig.DefaultInt("geo_reload_interval", 60); seconds > 0 {
		interval = time.Duration(seconds) * time.Second
	}
	go file.WatchAccessLists(ctx, interval)
	logs.Info("GeoIP/Geosite ACL 热加载已启用，检查周期：%s", interval)
}

func stopAccessListWatcher() {
	accessListMu.Lock()
	cancel := accessListCancel
	accessListCancel = nil
	accessListMu.Unlock()
	if cancel != nil {
		cancel()
	}
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

// prepareStartupConfig runs after SQLite has been verified. On a fresh
// install the full template never needs to exist on disk.
func prepareStartupConfig(runPath string, ini settings.StartupINI, values map[string]string) error {
	confDir := filepath.Join(runPath, "conf")
	confPath := filepath.Join(confDir, "nps.conf")
	onDisk, err := os.ReadFile(confPath)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return fmt.Errorf("读取启动配置: %w", err)
	}
	if !missing && string(onDisk) != ini.Content {
		return errors.New("nps.conf 在初始化时被修改，请重新启动")
	}
	content := ini.Content
	// Use effective settings (including includes) for defaults and aliases,
	// and put new top-level keys before any explicit sections.
	content, compatibilityAdded := migrateConfigCompatibilityWithValues(content, ini.Values)
	existing := make(map[string]struct{}, len(ini.Values)+len(compatibilityAdded))
	for key := range ini.Values {
		existing[key] = struct{}{}
	}
	for _, key := range compatibilityAdded {
		existing[key] = struct{}{}
	}
	content, _ = mergeMissingConfigFieldsWithKeys(content, defaultNpsConf, existing)
	content, secrets := rotateInsecureConfig(content)
	content, _ = settings.SlimStartupConfig(content, values, ini.RunMode)
	if missing || content != ini.Content {
		if err := writeConfigAtomically(confPath, []byte(content)); err != nil {
			return fmt.Errorf("保存精简启动配置: %w", err)
		}
		if missing {
			logs.Info("Auto-generated default config file:", confPath)
		}
		logGeneratedSecrets(secrets)
	}
	if err := beego.LoadAppConfig("ini", confPath); err != nil {
		return errors.New("无法加载精简后的 nps.conf，请检查配置格式和权限")
	}
	return nil
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
		{key: "auth_key", length: 32, defaults: []string{"123", "CHANGE_ME"}},
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
