//go:build !sdk
// +build !sdk

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/client"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/config"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/install"
	"ehang.io/nps/lib/quickcommand"
	"ehang.io/nps/lib/version"
	"github.com/astaxie/beego/logs"
	"github.com/ccding/go-stun/stun"
	"github.com/fatih/color"
	"github.com/kardianos/service"
)

var (
	serverAddr           = flag.String("server", "", "Server addr (ip:port)")
	configPath           = flag.String("config", "", "Configuration file path")
	verifyKey            = flag.String("vkey", "", "Authentication key")
	logType              = flag.String("log", "stdout", "Log output mode（stdout|file）")
	connType             = flag.String("type", "tcp", "Connection type with the server (tcp|kcp|ws|wss|quic)")
	bridgePath           = flag.String("bridge_path", "/ws", "WebSocket Bridge URL path")
	quicALPN             = flag.String("quic_alpn", "nps", "QUIC ALPN protocol")
	proxyUrl             = flag.String("proxy", "", "outbound proxy URL (http/https/socks4/socks4a/socks5/socks5h)")
	localIP              = flag.String("local_ip", "", "local source IP for outbound connections")
	localIPForward       = flag.Bool("local_ip_forward", false, "apply local_ip to tunnel forwarding egress for public targets")
	dnsServer            = flag.String("dns_server", "", "custom DNS server(s), comma separated")
	logLevel             = flag.String("log_level", "7", "log level 0~7")
	registerTime         = flag.Int("time", 2, "register time long /h")
	localPort            = flag.Int("local_port", 2000, "p2p local port")
	password             = flag.String("password", "", "p2p password flag")
	target               = flag.String("target", "", "p2p target")
	localType            = flag.String("local_type", "p2p", "p2p target")
	targetType           = flag.String("target_type", "all", "P2P target connection type (all|tcp|udp)")
	fallbackSecret       = flag.Bool("fallback_secret", true, "P2P fallback secret (true or false)")
	logPath              = flag.String("log_path", "", "npc log path")
	debug                = flag.Bool("debug", true, "npc debug")
	pprofAddr            = flag.String("pprof", "", "PProf debug addr (ip:port)")
	stunAddr             = flag.String("stun_addr", "stun.stunprotocol.org:3478", "stun server address (eg:stun.stunprotocol.org:3478)")
	ver                  = flag.Bool("version", false, "show current version")
	disconnectTime       = flag.Int("disconnect_timeout", 60, "not receiving check packet times, until timeout will disconnect the client")
	serviceName          = flag.String("service_name", "Npc", "system service name")
	remoteUpdateHelper   = flag.Bool("remote_update_helper", false, "internal Windows remote update helper")
	remoteUpdateTarget   = flag.String("remote_update_target", "", "internal remote update target executable")
	remoteUpdateParent   = flag.Int("remote_update_parent", 0, "internal remote update parent PID")
	remoteUpdateService  = flag.String("remote_update_service", "", "internal remote update service name")
	remoteUpdateArgsFile = flag.String("remote_update_args_file", "", "internal remote update argument file")
	disableRemoteUpdate  = flag.Bool("disable_remote_update", false, "ignore update requests from the server")
	tlsEnable            = flag.Bool("tls_enable", false, "enable tls")
	tlsCAFile            = flag.String("tls_ca_file", "", "TLS CA certificate file")
	tlsServerName        = flag.String("tls_server_name", "", "TLS server name")
	tlsFingerprint       = flag.String("tls_fingerprint", "", "TLS server SHA-256 certificate fingerprint")
	tlsInsecure          = flag.Bool("tls_insecure_skip_verify", false, "explicitly disable TLS certificate verification")
)

func currentTLSOptions() client.TLSOptions {
	return client.TLSOptions{
		CAFile:             *tlsCAFile,
		ServerName:         *tlsServerName,
		Fingerprint:        *tlsFingerprint,
		InsecureSkipVerify: *tlsInsecure,
		BridgePath:         *bridgePath,
		QuicALPN:           *quicALPN,
		LocalIP:            *localIP,
		LocalIPForward:     *localIPForward,
		DNSServer:          *dnsServer,
	}
}

func newNpcServiceConfig(name, displayName, description string) *service.Config {
	options := make(service.KeyValue)
	svcConfig := &service.Config{
		Name:        name,
		DisplayName: displayName,
		Description: description,
		Option:      options,
	}
	if !common.IsWindows() {
		svcConfig.Dependencies = []string{
			"Requires=network.target",
			"After=network-online.target syslog.target",
		}
		svcConfig.Option["SystemdScript"] = install.SystemdScript
		svcConfig.Option["SysvScript"] = install.SysvScript
	}
	return svcConfig
}

func findNpcCommand(args []string) (string, int) {
	for index, arg := range args {
		switch arg {
		case "status", "register", "update", "update-force", "nat", "start", "stop", "restart", "install", "uninstall":
			return arg, index
		}
	}
	return "", -1
}

func main() {
	flag.Parse()
	if *remoteUpdateHelper {
		runWindowsRemoteUpdateHelper()
		return
	}
	logs.Reset()
	logs.EnableFuncCallDepth(true)
	logs.SetLogFuncCallDepth(3)
	if *ver {
		common.PrintVersion()
		return
	}
	if *logPath == "" {
		*logPath = common.GetNpcLogPath()
	}
	if common.IsWindows() {
		*logPath = strings.Replace(*logPath, "\\", "\\\\", -1)
	}
	if *debug {
		logs.SetLogger(logs.AdapterConsole, `{"level":`+*logLevel+`,"color":true}`)
	} else {
		// Service logs are intentionally readable by non-root operators so the
		// built-in log viewer works for a user who can manage NPC services.
		// Beego defaults to 0660; a root-owned file with that mode otherwise
		// produces EACCES for ordinary users.
		logs.SetLogger(logs.AdapterFile, `{"level":`+*logLevel+`,"filename":"`+*logPath+`","daily":false,"maxlines":100000,"perm":"0644","rotateperm":"0644","color":true}`)
	}

	// init service
	svcConfig := newNpcServiceConfig(
		*serviceName,
		"nps内网穿透客户端",
		"一款轻量级、功能强大的内网穿透代理服务器。支持tcp、udp流量转发，支持内网http代理、内网socks5代理，同时支持snappy压缩、站点保护、加密传输、多路复用、header修改等。支持web图形化管理，集成多用户模式。",
	)
	for _, v := range os.Args[1:] {
		switch v {
		case "install", "start", "stop", "uninstall", "restart", "update", "update-force":
			continue
		}
		if !strings.Contains(v, "-service=") && !strings.Contains(v, "-debug=") {
			svcConfig.Arguments = append(svcConfig.Arguments, v)
		}
	}
	command, commandIndex := findNpcCommand(os.Args[1:])
	commandArgIndex := commandIndex + 1 // absolute index in os.Args
	svcConfig.Arguments = append(svcConfig.Arguments, "-debug=false")
	prg := &npc{
		exit: make(chan struct{}),
	}
	s, err := service.New(prg, svcConfig)
	if err != nil {
		logs.Error(err, "service function disabled")
		registerRemoteUpdateHandler(nil)
		run()
		// run without service
		wg := sync.WaitGroup{}
		wg.Add(1)
		wg.Wait()
		return
	}
	serviceStopper = npcServiceStopper{svc: s, prg: prg}
	registerRemoteUpdateHandler(s)
	if command != "" {
		switch command {
		case "status":
			if len(os.Args) > commandArgIndex+1 {
				path := strings.Replace(os.Args[commandArgIndex+1], "-config=", "", -1)
				client.GetTaskStatus(path)
			}
		case "register":
			flag.CommandLine.Parse(os.Args[commandArgIndex+1:])
			client.RegisterLocalIpWithTLS(*serverAddr, *verifyKey, *connType, *proxyUrl, *registerTime, *tlsEnable, currentTLSOptions())
		case "update":
			install.UpdateNpc()
			return
		case "update-force":
			install.ForceUpdateNpc()
			return
		case "nat":
			c := stun.NewClient()
			flag.CommandLine.Parse(os.Args[commandArgIndex+1:])
			c.SetServerAddr(*stunAddr)
			nat, host, err := c.Discover()
			if err != nil || host == nil {
				logs.Error("get nat type error", err)
				return
			}
			fmt.Printf("nat type: %s \npublic address: %s\n", nat.String(), host.String())
			os.Exit(0)
		case "start", "stop", "restart":
			// support busyBox and sysV, for openWrt
			if service.Platform() == "unix-systemv" {
				logs.Info("unix-systemv service")
				cmd := exec.Command("/etc/init.d/"+svcConfig.Name, command)
				err := cmd.Run()
				if err != nil {
					logs.Error(err)
				}
				return
			}
			err := service.Control(s, command)
			if err != nil {
				logs.Error("Valid actions: %q\n%s", service.ControlAction, err.Error())
			}
			return
		case "install":
			service.Control(s, "stop")
			service.Control(s, "uninstall")
			installedPath, installErr := install.InstallNpcPath()
			if installErr != nil {
				logs.Error(installErr, "install npc binary failed")
				return
			}
			// The service must run the stable installation path rather than the
			// archive/extraction path used to invoke this command.
			svcConfig.Executable = installedPath
			err := service.Control(s, command)
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
		case "uninstall":
			err := service.Control(s, command)
			if err != nil {
				logs.Error("Valid actions: %q\n%s", service.ControlAction, err.Error())
			}
			if service.Platform() == "unix-systemv" {
				logs.Info("unix-systemv service")
				os.Remove("/etc/rc.d/S90" + svcConfig.Name)
				os.Remove("/etc/rc.d/K02" + svcConfig.Name)
			}
			return
		}
	}
	s.Run()
}

func registerRemoteUpdateHandler(s service.Service) {
	if *disableRemoteUpdate {
		client.SetRemoteUpdateHandler(nil)
		return
	}
	client.SetRemoteUpdateHandler(func(vKey string) (bool, error) {
		if runtime.GOOS == "windows" {
			if s == nil {
				return false, fmt.Errorf("Windows 客户端服务对象不可用")
			}
			restartName := *serviceName
			if restartName == "Npc" && vKey != "" {
				// Older menu-installed services did not persist -service_name.
				restartName = "nps-client-" + vKey
			}
			serviceMode := !service.Interactive()
			if err := spawnWindowsRemoteUpdateHelper(restartName, serviceMode); err != nil {
				return false, fmt.Errorf("无法启动 Windows 客户端更新器：%w", err)
			}
			if serviceMode {
				// The helper waits for this process to exit before replacing the
				// executable, then starts the registered service again.
				if err := s.Stop(); err != nil {
					return false, fmt.Errorf("更新器已启动，但无法停止当前 Windows 服务：%w", err)
				}
				return true, nil
			}
			// A foreground process has no service manager to stop it. The
			// helper will relaunch the same command line after replacement.
			os.Exit(0)
			return true, nil
		}
		updated, err := install.UpdateNpcResult()
		if err != nil || !updated {
			return false, err
		}
		logs.Info("NPC update verified and installed; restarting client process")
		if err := install.RestartNpcProcess(); err != nil {
			return false, fmt.Errorf("客户端已更新，但自动重启失败：%w", err)
		}
		return true, nil
	})
}

// spawnWindowsRemoteUpdateHelper copies the current executable to a temporary
// updater path. Windows keeps the running image locked, so the helper waits
// for the NPC process to exit, updates the original executable, and then
// restarts either the registered service or the original foreground command.
func spawnWindowsRemoteUpdateHelper(name string, serviceMode bool) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	source, err := os.Open(executable)
	if err != nil {
		return err
	}
	helper, err := os.CreateTemp("", "nps-npc-updater-*.exe")
	if err != nil {
		_ = source.Close()
		return err
	}
	helperPath := helper.Name()
	if _, err := io.Copy(helper, source); err != nil {
		_ = source.Close()
		_ = helper.Close()
		_ = os.Remove(helperPath)
		return err
	}
	if err := source.Close(); err != nil {
		_ = helper.Close()
		_ = os.Remove(helperPath)
		return err
	}
	if err := helper.Close(); err != nil {
		_ = os.Remove(helperPath)
		return err
	}

	args := []string{
		"-remote_update_helper=true",
		"-remote_update_target=" + executable,
		"-remote_update_parent=" + strconv.Itoa(os.Getpid()),
	}
	var argsFile string
	if serviceMode {
		args = append(args, "-remote_update_service="+name)
	} else {
		file, createErr := os.CreateTemp("", "nps-npc-update-args-*.json")
		if createErr != nil {
			_ = os.Remove(helperPath)
			return createErr
		}
		argsFile = file.Name()
		if encodeErr := json.NewEncoder(file).Encode(os.Args[1:]); encodeErr != nil {
			_ = file.Close()
			_ = os.Remove(argsFile)
			_ = os.Remove(helperPath)
			return encodeErr
		}
		if closeErr := file.Close(); closeErr != nil {
			_ = os.Remove(argsFile)
			_ = os.Remove(helperPath)
			return closeErr
		}
		args = append(args, "-remote_update_args_file="+argsFile)
	}

	cmd := exec.Command(helperPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		if argsFile != "" {
			_ = os.Remove(argsFile)
		}
		_ = os.Remove(helperPath)
		return err
	}
	return cmd.Process.Release()
}

func runWindowsRemoteUpdateHelper() {
	if runtime.GOOS != "windows" {
		fmt.Fprintln(os.Stderr, "remote update helper is only supported on Windows")
		return
	}
	if *remoteUpdateTarget == "" {
		fmt.Fprintln(os.Stderr, "remote update helper target is missing")
		return
	}
	waitForWindowsProcessExit(*remoteUpdateParent)
	if _, err := install.UpdateNpcResultAt(*remoteUpdateTarget); err != nil {
		fmt.Fprintln(os.Stderr, "NPC 远程更新失败：", err)
		return
	}
	if *remoteUpdateService != "" {
		cmd := exec.Command(*remoteUpdateTarget, "-service_name="+*remoteUpdateService, "restart")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "NPC Windows 服务重启失败：", err)
		}
		return
	}
	if *remoteUpdateArgsFile == "" {
		fmt.Fprintln(os.Stderr, "remote update helper arguments are missing")
		return
	}
	data, err := os.ReadFile(*remoteUpdateArgsFile)
	_ = os.Remove(*remoteUpdateArgsFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取 NPC 启动参数失败：", err)
		return
	}
	var args []string
	if err := json.Unmarshal(data, &args); err != nil {
		fmt.Fprintln(os.Stderr, "解析 NPC 启动参数失败：", err)
		return
	}
	cmd := exec.Command(*remoteUpdateTarget, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "NPC 重启失败：", err)
	}
}

func waitForWindowsProcessExit(pid int) {
	if pid <= 0 {
		return
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		output, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH").CombinedOutput()
		if err != nil || !strings.Contains(string(output), strconv.Itoa(pid)) {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

type npc struct {
	exit     chan struct{}
	stopOnce sync.Once
}

type npcServiceStopper struct {
	once sync.Once
	svc  service.Service
	prg  *npc
}

var serviceStopper npcServiceStopper

var serviceConnGuard struct {
	sync.Mutex
	conns int
}

func registerNpcConn() {
	serviceConnGuard.Lock()
	serviceConnGuard.conns++
	serviceConnGuard.Unlock()
}

// popNpcConn decrements the live connection count and reports whether the
// terminating loop was the last one owned by this process.
func popNpcConn() bool {
	serviceConnGuard.Lock()
	serviceConnGuard.conns--
	last := serviceConnGuard.conns <= 0
	serviceConnGuard.Unlock()
	return last
}

// endNpcConn is called when one connection loop terminates. The process stops
// its own system service only when the last loop ended because the server
// deleted its registration, so deleting one client never tears down other
// tunnels served by the same process.
func endNpcConn(vKey string, kicked bool) {
	if !popNpcConn() || !kicked {
		return
	}
	stopNpcService(vKey)
}

// stopNpcService stops the system service owned by this process while leaving
// it installed so the operator can still manage it with the npc commands. It
// is intentionally best-effort and runs only once: a failed self-stop must
// never block or fail the server-side deletion.
func stopNpcService(vKey string) {
	serviceStopper.once.Do(func() {
		if service.Interactive() {
			logs.Info("[通知] 服务端已删除客户端（vkey:%s），客户端已停止", vKey)
			return
		}
		if serviceStopper.svc == nil || serviceStopper.prg == nil {
			logs.Info("[通知] 服务端已删除客户端（vkey:%s），当前进程未注册系统服务，客户端已停止", vKey)
			return
		}
		logs.Info("[通知] 服务端已删除客户端（vkey:%s），正在停止本机 NPC 系统服务", vKey)
		// systemctl/launchctl 等 stop 命令会等待本进程退出，因此异步发起；
		// 服务保持安装状态，之后仍可用 npc 命令手动启动或停止。
		go func() {
			if err := service.Control(serviceStopper.svc, "stop"); err != nil {
				logs.Error("[通知] 停止系统服务失败：%v", err)
			}
		}()
		if err := serviceStopper.prg.Stop(serviceStopper.svc); err != nil {
			logs.Error("[通知] 停止系统服务失败：%v", err)
		}
	})
}

func (p *npc) Start(s service.Service) error {
	go p.run()
	return nil
}
func (p *npc) Stop(s service.Service) error {
	p.stopOnce.Do(func() {
		close(p.exit)
	})
	if service.Interactive() {
		os.Exit(0)
	}
	return nil
}

func (p *npc) run() error {
	defer func() {
		if err := recover(); err != nil {
			const size = 64 << 10
			buf := make([]byte, size)
			buf = buf[:runtime.Stack(buf, false)]
			logs.Warning("npc: panic serving %v: %v\n%s", err, string(buf))
		}
	}()
	run()
	select {
	case <-p.exit:
		logs.Warning("stop...")
	}
	return nil
}

func run() {
	common.ReportInstall("npc")
	common.InitPProfFromArg(*pprofAddr)
	if err := common.SetCustomDNS(*dnsServer); err != nil {
		logs.Warn("[配置] DNS 服务器配置无效，继续使用当前系统解析器：%v", err)
	}
	//p2p or secret command
	if *password != "" {
		commonConfig := new(config.CommonConfig)
		commonConfig.Server = *serverAddr
		commonConfig.VKey = *verifyKey
		commonConfig.Tp = *connType
		commonConfig.TlsEnable = *tlsEnable
		commonConfig.TLSCAFile = *tlsCAFile
		commonConfig.TLSServerName = *tlsServerName
		commonConfig.TLSFingerprint = *tlsFingerprint
		commonConfig.TLSInsecureSkipVerify = *tlsInsecure
		commonConfig.QuicALPN = *quicALPN
		commonConfig.LocalIP = *localIP
		commonConfig.LocalIPForward = *localIPForward
		commonConfig.DnsServer = *dnsServer
		commonConfig.DisableRemoteUpdate = *disableRemoteUpdate
		localServer := new(config.LocalServer)
		localServer.Type = *localType
		localServer.Password = *password
		localServer.Target = *target
		localServer.Port = *localPort
		localServer.TargetType = strings.ToLower(*targetType)
		localServer.Fallback = *fallbackSecret
		commonConfig.Client = new(file.Client)
		commonConfig.Client.Cnf = new(file.Config)
		if version.SupportsModernP2P(version.VERSION) && (localServer.Type == "p2p" || localServer.Type == "secret") {
			// Keep one modern manager alive for the lifetime of the foreground
			// local listener. It owns the direct QUIC path, optional NAT port
			// mapping, and Secret fallback; the historical p2p/p2ps/p2pt
			// listeners remain available for explicitly legacy modes.
			manager := client.NewP2PManager(context.Background(), commonConfig)
			if err := manager.StartLocalServer(localServer); err != nil {
				logs.Error("启动 P2P/Secret 本地监听失败：", err)
				manager.Close()
				return
			}
			return
		}
		go client.StartLocalServer(localServer, commonConfig)
		return
	}
	env := common.GetEnvMap()
	if *serverAddr == "" {
		*serverAddr, _ = env["NPC_SERVER_ADDR"]
	}
	if *verifyKey == "" {
		*verifyKey, _ = env["NPC_SERVER_VKEY"]
	}
	if *verifyKey != "" && *serverAddr != "" && *configPath == "" {
		logs.Info("the version of client is %s, the core version of client is %s,tls enable is %t", version.VERSION, version.GetVersion(), *tlsEnable)
		connections := expandNpcConnections(*serverAddr, *verifyKey, *connType, *localIP)
		if len(connections) == 0 {
			logs.Error("server 或 vkey 不能为空")
			return
		}
		for _, connection := range connections {
			connection := connection
			go func() {
				registerNpcConn()
				kicked := false
				for {
					logs.Info("[连接] start server:%s vkey:%s type:%s local_ip:%s tls:%t", connection.server, connection.vkey, connection.transport, connection.localIP, connection.tls)
					options := currentTLSOptions()
					options.LocalIP = connection.localIP
					c := client.NewRPClientWithTLS(connection.server, connection.vkey, connection.transport, *proxyUrl, nil, *disconnectTime, connection.tls, options)
					c.Start()
					if c.Kicked() {
						kicked = true
						logs.Info("[通知] 服务端已删除客户端（server:%s vkey:%s），停止重连", connection.server, connection.vkey)
						break
					}
					logs.Info("[重连] 客户端连接已关闭，5 秒后重连")
					time.Sleep(time.Second * 5)
				}
				endNpcConn(connection.vkey, kicked)
			}()
		}

	} else {
		if *configPath == "" {
			*configPath = common.GetConfigPath()
		}

		// 配置文件可以用逗号分隔，同时启动多个独立的客户端配置。
		configPaths := strings.Split(strings.NewReplacer("，", ",").Replace(*configPath), ",")
		started := false
		for _, path := range configPaths {
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			if common.FileExists(path) {
				started = true
				logs.Info("配置文件模式启动：%s", path)
				go func() {
					registerNpcConn()
					var kickedVKey string
					kicked := false
					client.StartFromFile(path, func(vKey string) {
						kickedVKey = vKey
						kicked = true
					})
					endNpcConn(kickedVKey, kicked)
				}()
			} else if len(configPaths) > 1 {
				logs.Warn("配置文件不存在，跳过：%s", path)
			}
		}
		if started {
			return
		}
		// 无配置文件模式双击运行
		printSlogan()
		inputCmd()
	}
}

type npcConnection struct {
	server    string
	vkey      string
	transport string
	localIP   string
	tls       bool
}

// expandNpcConnections implements the multi-server command-line form used by
// current NPC deployments. A shorter vkey/type/local_ip list reuses its last
// value, so `-server=a:1,b:2 -vkey=key` is valid and deterministic.
func expandNpcConnections(servers, vkeys, transports, localIPs string) []npcConnection {
	split := func(value string) []string {
		value = strings.NewReplacer("，", ",", "：", ":").Replace(value)
		parts := strings.Split(value, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		for len(parts) > 0 && parts[len(parts)-1] == "" {
			parts = parts[:len(parts)-1]
		}
		return parts
	}
	serverList := split(servers)
	vkeyList := split(vkeys)
	transportList := split(transports)
	localIPList := split(localIPs)
	if len(serverList) == 0 || len(vkeyList) == 0 {
		return nil
	}
	if len(transportList) == 0 {
		transportList = []string{"tcp"}
	}
	maxLength := len(serverList)
	if len(vkeyList) > maxLength {
		maxLength = len(vkeyList)
	}
	if len(transportList) > maxLength {
		maxLength = len(transportList)
	}
	if len(localIPList) > maxLength {
		maxLength = len(localIPList)
	}
	valueAt := func(values []string, index int) string {
		if len(values) == 0 {
			return ""
		}
		if index < len(values) {
			return values[index]
		}
		return values[len(values)-1]
	}
	connections := make([]npcConnection, 0, maxLength)
	for index := 0; index < maxLength; index++ {
		transport := strings.ToLower(valueAt(transportList, index))
		tls := *tlsEnable
		if transport == "tls" {
			transport = "tcp"
			tls = true
		}
		connections = append(connections, npcConnection{
			server:    valueAt(serverList, index),
			vkey:      valueAt(vkeyList, index),
			transport: transport,
			localIP:   valueAt(localIPList, index),
			tls:       tls,
		})
	}
	return connections
}

func printSlogan() {
	green := color.New(color.FgGreen).SprintFunc()
	yellow := color.New(color.FgYellow).SprintFunc()

	_ = green

	fmt.Println()
	fmt.Printf("  %s\n", yellow("NPS 内网穿透客户端 v"+version.VERSION))
	fmt.Printf("  %s\n", "https://github.com/ZiDuNet/nps")
	fmt.Printf("  当前版本：%s\n", version.VERSION)
	fmt.Println()
	fmt.Println("  [1] 注册系统服务")
	fmt.Println("  [2] 删除/卸载系统服务")
	fmt.Println("  [3] 启动系统服务")
	fmt.Println("  [4] 停止系统服务")
	fmt.Println("  [5] 更新客户端")
	fmt.Println("  [6] 查看已注册服务列表")
	fmt.Println("  [7] 查看服务状态")
	fmt.Println("  [8] 查看 NPC 服务日志")
	fmt.Println("  [9] 重启所有 NPC 服务")
	fmt.Println("  [0] 退出")
	fmt.Println()
	fmt.Println("  输入[快捷启动命令]直接启动隧道，多个用英文逗号拼接")
	fmt.Println()
}

func inputCmd() {
	for {
		var input string
		fmt.Printf("请输入：")

		stdin := bufio.NewReader(os.Stdin)
		_, err := fmt.Fscanln(stdin, &input)
		if err != nil {
			fmt.Println("输入有误，请重新输入")
			continue
		}

		input = strings.Replace(input, " ", "", -1)

		if input == "0" {
			os.Exit(0)
		}

		if input == "1" || input == "2" || input == "3" || input == "4" {
			systemService(input)
			continue
		}

		if input == "5" {
			install.UpdateNpcNew()
			continue
		}

		if input == "6" {
			printNpcServiceList()
			continue
		}

		if input == "7" {
			systemService(input)
			continue
		}

		if input == "8" {
			showNpcServiceLogs()
			continue
		}

		if input == "9" {
			restartAllNpcServices()
			continue
		}

		// 快捷启动命令模式
		vkeys := strings.Split(input, ",")

		commands := make([]quickcommand.Command, 0, len(vkeys))
		for _, key := range vkeys {
			command, err := quickcommand.DecodeBase64(key)
			if err != nil {
				fmt.Printf("快捷启动命令解析失败：%v\n", err)
				commands = nil
				break
			}
			commands = append(commands, command)
		}

		if len(commands) == 0 {
			continue
		}

		for _, command := range commands {
			startNpcServer(command)
		}
		fmt.Println("隧道已启动，连接中...")
	}
}

func startNpcServer(command quickcommand.Command) {
	command, err := quickcommand.Normalize(command)
	if err != nil {
		logs.Error("快捷启动命令格式错误：%v", err)
		return
	}

	go func() {
		registerNpcConn()
		kicked := false
		defer func() {
			endNpcConn(command.VKey, kicked)
		}()
		for {
			logs.Info("start cmd:%s", command.CommandLine("npc"))
			logs.Info("版本: %s, 核心版本: %s, 协议: %s, TLS: %t", version.VERSION, version.GetVersion(), command.ProtocolName(), command.TLS)
			options := tlsOptionsForCommand(command)
			c := client.NewRPClientWithTLS(command.Server, command.VKey, command.Transport, *proxyUrl, nil, *disconnectTime, command.TLS, options)
			c.Start()
			if c.Kicked() {
				kicked = true
				logs.Info("[通知] 服务端已删除本客户端，停止重连")
				return
			}
			logs.Info("[重连] 连接断开，5 秒后重连")
			time.Sleep(time.Second * 5)
		}
	}()
}

func tlsOptionsForCommand(command quickcommand.Command) client.TLSOptions {
	options := currentTLSOptions()
	options.CAFile = command.TLSCAFile
	options.ServerName = command.TLSServerName
	options.Fingerprint = command.TLSFingerprint
	options.InsecureSkipVerify = command.TLSInsecureSkipVerify
	options.BridgePath = command.BridgePath
	options.QuicALPN = command.QuicALPN
	return options
}

func systemService(flag string) {
	for {
		if flag == "1" {
			fmt.Printf("请输入[快捷启动命令]，多个用英文逗号拼接：")
		} else {
			fmt.Printf("请输入[VKEY 或完整服务名]，多个用英文逗号拼接：")
		}

		var input string
		stdin := bufio.NewReader(os.Stdin)
		_, err := fmt.Fscanln(stdin, &input)

		if err != nil {
			fmt.Println("输入错误，请重新输入")
			continue
		}

		input = strings.Replace(input, " ", "", -1)

		if input == "0" {
			os.Exit(0)
		}

		vkeys := strings.Split(input, ",")

		if flag == "1" {
			commands := make([]quickcommand.Command, 0, len(vkeys))
			for _, key := range vkeys {
				command, err := quickcommand.DecodeBase64(key)
				if err != nil {
					fmt.Printf("快捷启动命令解析失败：%v\n", err)
					commands = nil
					break
				}
				commands = append(commands, command)
			}

			if len(commands) == 0 {
				continue
			}

			for _, command := range commands {
				systemPro(flag, npcServiceName(command), command)
			}
		} else {
			for _, key := range vkeys {
				names, err := listNpcServiceNames()
				if err != nil {
					fmt.Println("获取服务列表失败：", err)
					continue
				}
				serviceName, err := resolveNpcServiceName(names, key)
				if err != nil {
					fmt.Println("无法操作系统服务：", err)
					continue
				}
				command := quickcommand.Command{
					VKey:      strings.TrimPrefix(serviceName, "nps-client-"),
					Transport: "tcp",
				}
				systemPro(flag, serviceName, command)
			}
		}

		return
	}
}

func npcServiceName(command quickcommand.Command) string {
	name := "nps-client-" + strings.TrimSpace(command.VKey)
	if suffix := command.ServiceSuffix(); suffix != "" && suffix != "invalid" {
		name += "-" + suffix
	}
	return name
}

func systemPro(flag string, serviceName string, command quickcommand.Command) {
	// init service
	prg := &npc{
		exit: make(chan struct{}),
	}
	vkey := strings.TrimSpace(command.VKey)
	svcConfig := newNpcServiceConfig(
		serviceName,
		serviceName,
		"NPS内网穿透客户端，支持tcp、udp流量转发，支持内网http代理，地址：https://github.com/ZiDuNet/nps",
	)

	if flag == "1" {
		svcConfig.Arguments = append(svcConfig.Arguments, "-service_name="+svcConfig.Name)
		svcConfig.Arguments = append(svcConfig.Arguments, command.Args()...)
		svcConfig.Arguments = append(svcConfig.Arguments, "-debug=false")

		*logPath = common.GetNpcLogPath()
		if common.IsWindows() {
			*logPath = strings.Replace(*logPath, "\\", "\\\\", -1)
		}

		logName := "npc-" + strings.TrimPrefix(serviceName, "nps-client-") + ".log"
		*logPath = strings.Replace(*logPath, "npc.log", logName, -1)
		svcConfig.Arguments = append(svcConfig.Arguments, "-log_path="+*logPath)

		logs.NewLogger()
		logs.Reset()
		logs.EnableFuncCallDepth(true)
		logs.SetLogFuncCallDepth(3)
		logs.SetLogger(logs.AdapterFile, `{"level":`+*logLevel+`,"filename":"`+*logPath+`","daily":false,"maxlines":100000,"perm":"0644","rotateperm":"0644","color":true}`)

		installedPath, installErr := install.InstallNpcPath()
		if installErr != nil {
			fmt.Println("隧道["+vkey+"]安装客户端失败", installErr)
			return
		}
		// Use the stable installed binary in systemd/SysV scripts. Otherwise a
		// service created from a temporary extraction directory breaks after the
		// downloaded archive is moved or removed.
		svcConfig.Executable = installedPath
	}

	s, err := service.New(prg, svcConfig)
	if err != nil {
		fmt.Println("隧道["+vkey+"]系统服务管理器不可用", err)
		return
	}

	switch flag {
	case "1":
		err := service.Control(s, "install")
		if err != nil {
			fmt.Println("隧道["+vkey+"]安装到系统服务失败", err)
			return
		} else {
			fmt.Println("隧道[" + vkey + "]已经安装到系统")
		}
		if service.Platform() == "unix-systemv" {
			logs.Info("unix-systemv service")
			confPath := "/etc/init.d/" + svcConfig.Name
			os.Symlink(confPath, "/etc/rc.d/S90"+svcConfig.Name)
			os.Symlink(confPath, "/etc/rc.d/K02"+svcConfig.Name)
		}

		err2 := service.Control(s, "start")
		if err2 != nil {
			fmt.Println("隧道["+vkey+"]启动服务失败", err2)
		} else {
			fmt.Println("隧道[" + vkey + "]服务已启动")
		}

		return
	case "2":
		// 卸载系统服务
		err := service.Control(s, "stop")
		if err != nil {
			fmt.Println("隧道["+vkey+"]服务停止失败", err)
		} else {
			fmt.Println("隧道[" + vkey + "]服务已停止")
		}

		err = service.Control(s, "uninstall")
		if err != nil {
			fmt.Println("隧道["+vkey+"]服务卸载失败", err)
		}
		if service.Platform() == "unix-systemv" {
			fmt.Println("unix-systemv service")
			os.Remove("/etc/rc.d/S90" + svcConfig.Name)
			os.Remove("/etc/rc.d/K02" + svcConfig.Name)
		}

		if err == nil {
			fmt.Println("隧道[" + vkey + "]服务已卸载成功")
		}

		return

	case "3":
		//启动系统服务
		if service.Platform() == "unix-systemv" {
			logs.Info("unix-systemv service")
			cmd := exec.Command("/etc/init.d/"+svcConfig.Name, "start")
			err := cmd.Run()
			if err != nil {
				logs.Error(err)
			}
			return
		}
		err := service.Control(s, "start")
		if err != nil {
			fmt.Println("隧道["+vkey+"]服务启动失败", err)
		} else {
			fmt.Println("隧道[" + vkey + "]服务启动成功")
		}

		return
	case "4":
		if service.Platform() == "unix-systemv" {
			logs.Info("unix-systemv service")
			cmd := exec.Command("/etc/init.d/"+svcConfig.Name, "stop")
			err := cmd.Run()
			if err != nil {
				logs.Error(err)
			}
			return
		}
		err := service.Control(s, "stop")
		if err != nil {
			fmt.Println("隧道["+vkey+"]服务停止失败", err)
		} else {
			fmt.Println("隧道[" + vkey + "]服务停止成功")
		}

		return
	case "7":
		status, err := s.Status()
		fmt.Println("隧道[" + vkey + "]服务状态：" + serviceStatusText(status, err))
		return
	}
}

func printNpcServiceList() {
	names, err := listNpcServiceNames()
	if err != nil {
		fmt.Println("获取服务列表失败：", err)
		return
	}
	if len(names) == 0 {
		fmt.Println("未发现已注册的 nps-client-* 服务")
		return
	}

	fmt.Println("已注册服务列表：")
	for _, name := range names {
		status, err := npcServiceStatus(name)
		fmt.Println("  " + name + "：" + serviceStatusText(status, err))
	}
}

func showNpcServiceLogs() {
	names, err := listNpcServiceNames()
	if err != nil {
		fmt.Println("获取服务列表失败：", err)
		return
	}
	if len(names) == 0 {
		fmt.Println("未发现已注册的 nps-client-* 服务")
		return
	}

	fmt.Println("可查看日志的 NPC 服务：")
	for _, name := range names {
		fmt.Println("  " + name)
	}
	fmt.Print("请输入 VKEY 或服务名（输入 0 返回）：")
	var input string
	if _, err := fmt.Fscanln(bufio.NewReader(os.Stdin), &input); err != nil {
		fmt.Println("读取输入失败：", err)
		return
	}
	if input == "0" {
		return
	}

	serviceName, err := resolveNpcServiceName(names, strings.TrimSpace(input))
	if err != nil {
		fmt.Println("无法查看日志：", err)
		return
	}
	logPath, err := npcServiceLogPath(serviceName)
	if err != nil {
		fmt.Println("无法查看日志：", err)
		return
	}
	fmt.Printf("正在查看 %s 的日志（Ctrl+C 返回菜单）：%s\n", serviceName, logPath)
	if err := followNpcLogFile(logPath); err != nil {
		fmt.Println("读取日志失败：", err)
	}
}

func resolveNpcServiceName(names []string, input string) (string, error) {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "nps-client-") {
		for _, name := range names {
			if input == name {
				return name, nil
			}
		}
		return "", fmt.Errorf("服务 %q 未注册", input)
	}
	if input == "" || strings.ContainsAny(input, `/\\`) || input == "." || input == ".." {
		return "", fmt.Errorf("请输入有效的 VKEY")
	}
	wanted := "nps-client-" + input
	for _, name := range names {
		if wanted == name {
			return name, nil
		}
	}
	var protocolMatches []string
	for _, name := range names {
		if strings.HasPrefix(name, wanted+"-") {
			protocolMatches = append(protocolMatches, name)
		}
	}
	if len(protocolMatches) == 1 {
		return protocolMatches[0], nil
	}
	if len(protocolMatches) > 1 {
		sort.Strings(protocolMatches)
		return "", fmt.Errorf("VKEY %q 存在多个协议服务，请填写完整服务名：%s", input, strings.Join(protocolMatches, ", "))
	}
	return "", fmt.Errorf("VKEY %q 对应的服务未注册", input)
}

func npcServiceLogPath(serviceName string) (string, error) {
	if !strings.HasPrefix(serviceName, "nps-client-") || strings.ContainsAny(serviceName, `/\\`) {
		return "", fmt.Errorf("无效的 NPC 服务名 %q", serviceName)
	}
	vkey := strings.TrimPrefix(serviceName, "nps-client-")
	if vkey == "" || vkey == "." || vkey == ".." {
		return "", fmt.Errorf("无效的 NPC 服务名 %q", serviceName)
	}
	return filepath.Join(filepath.Dir(common.GetNpcLogPath()), "npc-"+vkey+".log"), nil
}

func followNpcLogFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("日志文件权限不足（%s），请先重启对应 NPC 服务以应用新的 0644 日志权限，或使用 root/管理员运行 NPC 菜单：%w", path, err)
		}
		return fmt.Errorf("日志文件不存在或无法读取（%s）：%w", path, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	const initialTailBytes = 128 * 1024
	start := info.Size() - initialTailBytes
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReader(file)
	if start > 0 {
		if _, err := reader.ReadString('\n'); err != nil && err != io.EOF {
			return err
		}
	}
	initial, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(initial), "\n"), "\n")
	if len(initial) == 0 {
		lines = nil
	}
	if len(lines) > 100 {
		lines = lines[len(lines)-100:]
	}
	for _, line := range lines {
		fmt.Println(line)
	}

	// Keep the interactive menu alive when the user presses Ctrl+C.
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	defer signal.Stop(interrupt)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			fmt.Print(line)
		}
		if readErr == nil {
			continue
		}
		if readErr != io.EOF {
			return readErr
		}

		select {
		case <-interrupt:
			fmt.Println("\n已退出日志查看")
			return nil
		case <-ticker.C:
			currentOffset, seekErr := file.Seek(0, io.SeekCurrent)
			if seekErr != nil {
				return seekErr
			}
			currentInfo, statErr := file.Stat()
			if statErr != nil {
				return statErr
			}
			if currentInfo.Size() < currentOffset {
				if _, err := file.Seek(0, io.SeekStart); err != nil {
					return err
				}
				reader.Reset(file)
				fmt.Println("日志文件已被清空，继续跟踪新日志")
			}
		}
	}
}

func restartAllNpcServices() {
	names, err := listNpcServiceNames()
	if err != nil {
		fmt.Println("获取服务列表失败：", err)
		return
	}
	if len(names) == 0 {
		fmt.Println("未发现已注册的 nps-client-* 服务")
		return
	}

	fmt.Printf("即将重启以下 %d 个 NPC 服务：\n", len(names))
	for _, name := range names {
		fmt.Println("  " + name)
	}
	fmt.Print("重启会短暂中断这些隧道，确认继续？(y/N)：")
	var confirmation string
	if _, err := fmt.Fscanln(bufio.NewReader(os.Stdin), &confirmation); err != nil || !strings.EqualFold(strings.TrimSpace(confirmation), "y") {
		fmt.Println("已取消")
		return
	}

	failed := 0
	for _, name := range names {
		if err := controlNpcService(name, "restart"); err != nil {
			failed++
			fmt.Printf("  %s：重启失败：%v\n", name, err)
		} else {
			fmt.Printf("  %s：重启成功\n", name)
		}
	}
	fmt.Printf("重启完成：成功 %d 个，失败 %d 个\n", len(names)-failed, failed)
}

func controlNpcService(name, action string) error {
	if !strings.HasPrefix(name, "nps-client-") || strings.ContainsAny(name, `/\\`) {
		return fmt.Errorf("无效的 NPC 服务名 %q", name)
	}
	if service.Platform() == "unix-systemv" {
		cmd := exec.Command("/etc/init.d/"+name, action)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}

	prg := &npc{exit: make(chan struct{})}
	svcConfig := newNpcServiceConfig(name, name, "NPS 内网穿透客户端")
	s, err := service.New(prg, svcConfig)
	if err != nil {
		return err
	}
	return service.Control(s, action)
}

func listNpcServiceNames() ([]string, error) {
	if common.IsWindows() {
		return listNpcServiceNamesByCommand("powershell", "-NoProfile", "-Command", "Get-Service -Name 'nps-client-*' -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Name; exit 0")
	}

	if runtime.GOOS == "darwin" {
		homeDir, _ := os.UserHomeDir()
		patterns := []string{
			"/Library/LaunchDaemons/nps-client-*.plist",
		}
		if homeDir != "" {
			patterns = append(patterns, filepath.Join(homeDir, "Library", "LaunchAgents", "nps-client-*.plist"))
		}
		return listNpcServiceNamesByGlob(patterns, ".plist")
	}

	return listNpcServiceNamesByGlob([]string{
		"/etc/systemd/system/nps-client-*.service",
		"/etc/init.d/nps-client-*",
	}, ".service")
}

func listNpcServiceNamesByGlob(patterns []string, suffix string) ([]string, error) {
	names := make(map[string]struct{})
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			name := strings.TrimSuffix(filepath.Base(match), suffix)
			if strings.HasPrefix(name, "nps-client-") {
				names[name] = struct{}{}
			}
		}
	}
	return sortedServiceNames(names), nil
}

func listNpcServiceNamesByCommand(name string, args ...string) ([]string, error) {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return nil, err
	}

	names := make(map[string]struct{})
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "nps-client-") {
			names[line] = struct{}{}
		}
	}
	return sortedServiceNames(names), nil
}

func sortedServiceNames(names map[string]struct{}) []string {
	list := make([]string, 0, len(names))
	for name := range names {
		list = append(list, name)
	}
	sort.Strings(list)
	return list
}

func npcServiceStatus(name string) (service.Status, error) {
	prg := &npc{
		exit: make(chan struct{}),
	}
	svcConfig := &service.Config{
		Name:        name,
		DisplayName: name,
	}
	s, err := service.New(prg, svcConfig)
	if err != nil {
		return service.StatusUnknown, err
	}
	return s.Status()
}

func serviceStatusText(status service.Status, err error) string {
	if err != nil {
		return "未安装或状态未知"
	}
	switch status {
	case service.StatusRunning:
		return "运行中"
	case service.StatusStopped:
		return "已停止"
	default:
		return "未知"
	}
}
