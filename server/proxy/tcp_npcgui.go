//go:build npcgui
// +build npcgui

package proxy

import (
	"errors"
	"net/http"

	"ehang.io/nps/bridge"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego"
)

// GUI 客户端构建（-tags npcgui）不包含 tcp.go 中的完整隧道服务，
// 此处提供占位实现，保证 client/local.go 等引用可以编译。

type process func(c *conn.Conn, s *TunnelModeServer) error

type TunnelModeServer struct{}

func NewTunnelModeServer(p process, bridge NetBridge, task *file.Tunnel) *TunnelModeServer {
	return new(TunnelModeServer)
}

func (s *TunnelModeServer) Start() error {
	return errors.New("tunnel mode server is not available in GUI build")
}

func (s *TunnelModeServer) Close() error {
	return nil
}

// The root module still compiles the server package when npcgui is enabled.
// Keep the server-only entry points available without pulling the full TCP
// proxy implementation into the GUI client binary.
func ProcessTunnel(c *conn.Conn, s *TunnelModeServer) error {
	if c != nil {
		_ = c.Close()
	}
	return errors.New("tunnel mode server is not available in GUI build")
}

func ProcessHttp(c *conn.Conn, s *TunnelModeServer) error {
	if c != nil {
		_ = c.Close()
	}
	return errors.New("http proxy server is not available in GUI build")
}

type WebServer struct{}

func NewWebServer(bridge *bridge.Bridge) *WebServer {
	return new(WebServer)
}

func (s *WebServer) Start() error {
	return errors.New("web management server is not available in GUI build")
}

func (s *WebServer) Close() error {
	return nil
}

func configureWebSessionTLS(useTLS bool) {
	beego.BConfig.Listen.EnableHTTPS = useTLS
}

func newManagedHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: httpReadHeaderTimeout, IdleTimeout: httpIdleTimeout}
}
