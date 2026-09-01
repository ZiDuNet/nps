package connection

import (
	"net"
	"os"
	"strconv"
	"strings"

	"ehang.io/nps/lib/pmux"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"
)

var pMux *pmux.PortMux
var bridgePort string
var httpsPort string
var httpPort string
var webPort string

func InitConnectionService() {
	bridgePort = beego.AppConfig.String("bridge_port")
	httpsPort = beego.AppConfig.String("https_proxy_port")
	httpPort = beego.AppConfig.String("http_proxy_port")
	webPort = beego.AppConfig.String("web_port")

	if httpPort == bridgePort || httpsPort == bridgePort || webPort == bridgePort {
		port, err := strconv.Atoi(bridgePort)
		if err != nil {
			logs.Error(err)
			return
		}
		pMux = pmux.NewPortMuxWithAddress(port, beego.AppConfig.String("web_host"), beego.AppConfig.String("bridge_ip"))
		if err := pMux.StartError(); err != nil {
			logs.Error("start port multiplexer failed: %v", err)
			pMux = nil
		}
	}
}

func GetBridgeListener(tp string) (net.Listener, error) {
	logs.Info("server start, the bridge type is %s, the bridge port is %s", tp, bridgePort)
	var p int
	var err error
	if p, err = strconv.Atoi(bridgePort); err != nil {
		return nil, err
	}
	if pMux != nil {
		return pMux.GetClientListener(), nil
	}
	return getTcpListener(beego.AppConfig.String("bridge_ip"), strconv.Itoa(p))
}

func GetHttpListener() (net.Listener, error) {
	if pMux != nil && httpPort == bridgePort {
		logs.Info("start http listener, port is", bridgePort)
		return pMux.GetHttpListener(), nil
	}
	logs.Info("start http listener, port is", httpPort)
	return getTcpListener(beego.AppConfig.String("http_proxy_ip"), httpPort)
}

func GetHttpsListener() (net.Listener, error) {
	if pMux != nil && httpsPort == bridgePort {
		logs.Info("start https listener, port is", bridgePort)
		return pMux.GetHttpsListener(), nil
	}
	logs.Info("start https listener, port is", httpsPort)
	return getTcpListener(beego.AppConfig.String("http_proxy_ip"), httpsPort)
}

func GetWebManagerListener() (net.Listener, error) {
	if pMux != nil && webPort == bridgePort {
		logs.Info("Web management start, access port is", bridgePort)
		return pMux.GetManagerListener(), nil
	}
	logs.Info("web management start, access port is", webPort)
	return getTcpListener(WebManagerIP(), webPort)
}

// WebManagerIP returns the configured Web management listener address.  The
// environment override is intentionally narrow: container deployments can
// bind inside the container without changing the persisted, secure default.
func WebManagerIP() string {
	if ip := strings.TrimSpace(os.Getenv("NPS_WEB_IP")); ip != "" {
		return ip
	}
	return strings.TrimSpace(beego.AppConfig.String("web_ip"))
}

// WebManagerUsesPortMultiplexing reports whether the management panel shares
// the bridge listener. In that mode the bridge bind address controls panel
// exposure rather than web_ip.
func WebManagerUsesPortMultiplexing() bool {
	return pMux != nil && webPort == bridgePort
}

func getTcpListener(ip, p string) (net.Listener, error) {
	port, err := strconv.Atoi(p)
	if err != nil {
		logs.Error(err)
		return nil, err
	}
	if ip == "" {
		ip = "0.0.0.0"
	}
	// net.ParseIP returns nil for hostnames and malformed input. Passing that
	// nil to net.ListenTCP silently turns the listener into a wildcard bind.
	// Let net.Listen resolve valid hostnames and reject invalid addresses.
	return net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
}
