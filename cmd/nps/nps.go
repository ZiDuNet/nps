package main

import (
	"bufio"
	"ehang.io/nps/bridge"
	"ehang.io/nps/lib/daemon"
	"ehang.io/nps/server"
	"flag"
	"fmt"
	"github.com/fatih/color"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"

	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/install"
	"ehang.io/nps/lib/version"
	"ehang.io/nps/server/connection"
	"ehang.io/nps/server/tool"
	"ehang.io/nps/web/routers"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/web"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"

	"github.com/kardianos/service"
)

var (
	level      string
	ver        = flag.Bool("version", false, "show current version")
	confPath   = flag.String("conf_path", "", "set current confPath")
	serverCmd  = flag.Bool("server", false, "NPS管理脚本")
	npsLogPath = flag.String("log_path", "", "nps log path")
)

func main() {

	debug.SetMaxThreads(1000000)

	flag.Parse()
	// init log
	if *ver {
		common.PrintVersion()
		return
	}
	if *serverCmd {
		printSlogan()
		inputCmd()
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

	// auto-generate default config files if not exist
	initConfig(filepath.Join(common.GetRunPath(), "conf"))

	if err := beego.LoadAppConfig("ini", filepath.Join(common.GetRunPath(), "conf", "nps.conf")); err != nil {
		log.Fatalln("load config file error", err.Error())
	}

	common.InitPProfFromFile()
	if level = beego.AppConfig.String("log_level"); level == "" {
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
		if common.IsWindows() {
			logPath = strings.Replace(logPath, "\\", "\\\\", -1)
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
		_ = logs.SetLogger(logs.AdapterFile, `{"level":`+level+`,"filename":"`+logPath+`","daily":false,"maxlines":100000,"color":true}`)
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
		run()
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
			install.UpdateNps()
			return
			//default:
			//	logs.Error("command is not support")
			//	return
		}
	}

	_ = s.Run()
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
			install.UpdateNpsNew()
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

func installNps() {

}

type nps struct {
	exit chan struct{}
}

func (p *nps) Start(s service.Service) error {
	_, _ = s.Status()
	go p.run()
	return nil
}
func (p *nps) Stop(s service.Service) error {
	_, _ = s.Status()
	close(p.exit)
	if service.Interactive() {
		os.Exit(0)
	}
	return nil
}

func (p *nps) run() error {
	defer func() {
		if err := recover(); err != nil {
			const size = 64 << 10
			buf := make([]byte, size)
			buf = buf[:runtime.Stack(buf, false)]
			logs.Warning("nps: panic serving %v: %v\n%s", err, string(buf))
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
	routers.Init()
	task := &file.Tunnel{
		Mode: "webServer",
	}
	bridgePort, err := beego.AppConfig.Int("bridge_port")
	if err != nil {
		logs.Error("Getting bridge_port error", err)
		os.Exit(0)
	}

	logs.Info("日志路径：" + *npsLogPath)
	logs.Info("the config path is:" + common.GetRunPath())
	logs.Info("the version of server is %s ,allow client core version to be %s,tls enable is %t", version.VERSION, version.GetVersion(), bridge.ServerTlsEnable)
	connection.InitConnectionService()
	//crypt.InitTls(filepath.Join(common.GetRunPath(), "conf", "server.pem"), filepath.Join(common.GetRunPath(), "conf", "server.key"))
	crypt.InitTls()
	tool.InitAllowPort()
	tool.StartSystemInfo()
	common.ReportInstall("nps")
	timeout, err := beego.AppConfig.Int("disconnect_timeout")
	if err != nil {
		timeout = 60
	}
	go server.StartNewServer(bridgePort, task, beego.AppConfig.String("bridge_type"), timeout)
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
	} else if content, err := os.ReadFile(confPath); err == nil {
		updated, secrets := rotateInsecureConfig(string(content))
		if len(secrets) > 0 {
			if err := os.WriteFile(confPath, []byte(updated), 0600); err != nil {
				logs.Error("rotate insecure config secrets failed:", err)
			} else {
				_ = os.Chmod(confPath, 0600)
				logs.Warn("rotated known default config secrets in:", confPath)
				logGeneratedSecrets(secrets)
			}
		} else {
			_ = os.Chmod(confPath, 0600)
		}
	} else {
		logs.Warn("read config file for secret check failed:", err)
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

const defaultNpsConf = `###########################################################################
# HTTP(S) 代理配置
###########################################################################
# HTTP/HTTPS 代理监听 IP，0.0.0.0 表示监听服务器所有网卡
http_proxy_ip=0.0.0.0
# HTTP 代理监听端口；留空则不启动 HTTP 主机代理
http_proxy_port=80
# HTTPS 代理监听端口；留空则不启动 HTTPS 主机代理
https_proxy_port=443
# 管理面板中是否展示非 80 端口的 HTTP 访问地址
show_http_proxy_port=true

###########################################################################
# Bridge 配置（NPC 与 NPS 的控制连接）
###########################################################################
# Bridge 通信协议，可选 tcp 或 kcp
bridge_type=tcp
# Bridge 服务监听端口；NPC 的 -server 参数应使用此端口
bridge_port=8024
# Bridge 服务监听 IP，0.0.0.0 表示允许所有网卡接入
bridge_ip=0.0.0.0

###########################################################################
# 客户端认证与连接限制
###########################################################################
# 客户端连接服务端的公共验证密钥；留空则禁止使用公共密钥连接
public_vkey=
# 是否按客户端来源 IP 限制连接；true 启用、false 禁用，留空则忽略
#ip_limit=true

###########################################################################
# 流量与日志
###########################################################################
# 流量数据持久化间隔，单位为分钟；留空或 0 表示不持久化
flow_store_interval=1
# 日志级别：0 紧急、1 警报、2 严重、3 错误、4 警告、5 通知、6 信息、7 调试
log_level=6
# 日志文件路径；相对路径以程序运行目录为准
log_path=nps.log

###########################################################################
# P2P 中继配置（可选，取消注释后启用）
###########################################################################
# P2P 中继监听 IP；对外服务时通常设为 0.0.0.0
#p2p_ip=127.0.0.1
# P2P 中继监听端口；配置后才会启动 P2P 服务
#p2p_port=6000

###########################################################################
# 管理面板配置
###########################################################################
# 管理面板对应的域名；端口复用场景用于区分面板流量
web_host=a.o.com
# 管理面板管理员用户名
web_username=admin
# 管理面板管理员密码；首次启动时会自动生成随机值
web_password=CHANGE_ME
# 管理面板监听端口
web_port = 8081
# 管理面板监听 IP，0.0.0.0 表示允许外部访问
web_ip=0.0.0.0
# 管理面板 URL 前缀；反向代理到子路径时填写，例如 /nps
web_base_url=
# 是否为管理面板启用 HTTPS
web_open_ssl=false
# 管理面板 HTTPS 证书文件路径；web_open_ssl=true 时生效
web_cert_file=conf/server.pem
# 管理面板 HTTPS 私钥文件路径；web_open_ssl=true 时生效
web_key_file=conf/server.key

###########################################################################
# Web API 认证配置
###########################################################################
# Web API 认证密钥；首次启动时会自动生成随机值
auth_key=CHANGE_ME
# 获取服务端 auth_key 使用的 AES 密钥，必须恰好为 16 个字符；首次启动时会自动生成
auth_crypt_key =CHANGE_ME_16CHAR

###########################################################################
# 端口权限配置（可选）
###########################################################################
# 限制客户端可映射的端口范围，例如 9001-9009,10001,11000-12000
#allow_ports=9001-9009,10001,11000-12000

###########################################################################
# 管理面板多用户配置
###########################################################################
# 是否允许普通用户登录管理面板
allow_user_login=true
# 是否允许普通用户自行注册管理面板账号
allow_user_register=false
# 是否允许普通用户修改自己的用户名
allow_user_change_username=true

###########################################################################
# 客户端资源与代理功能开关
###########################################################################
# 是否允许在管理面板为客户端设置总流量上限
allow_flow_limit=true
# 是否允许在管理面板为客户端设置带宽上限
allow_rate_limit=true
# 是否允许在管理面板为客户端设置最大隧道数
allow_tunnel_num_limit=true
# 是否允许客户端使用本地代理访问目标地址
allow_local_proxy=false
# 是否允许在管理面板为客户端设置最大连接数
allow_connection_num_limit=true
# 是否允许每个隧道绑定不同的服务端监听 IP
allow_multi_ip=true
# 是否在管理面板仪表盘显示 CPU、内存和网络等系统信息
system_info_display=true

###########################################################################
# HTTP 转发与缓存
###########################################################################
# 是否向后端请求添加客户端真实 IP 相关请求头
http_add_origin_header=true
# 是否启用 HTTP 响应缓存
http_cache=false
# HTTP 缓存队列长度；仅在 http_cache=true 时生效
http_cache_length=100

###########################################################################
# 性能调试（可选，仅建议在内网临时开启）
###########################################################################
# pprof 监听 IP
#pprof_ip=127.0.0.1
# pprof 监听端口
#pprof_port=9999

###########################################################################
# 连接保活与 TLS
###########################################################################
# 客户端断线超时时间，单位为秒
disconnect_timeout=60
# 是否在管理面板登录时启用验证码
open_captcha=false
# 是否启用 Bridge TLS 加密监听
tls_enable=true
# Bridge TLS 加密监听端口；tls_enable=true 时生效
tls_bridge_port=8025
`
