//go:build !npcgui
// +build !npcgui

package proxy

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/bridge"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/server/connection"
	"ehang.io/nps/web"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"
)

type TunnelModeServer struct {
	BaseServer
	process    process
	listener   net.Listener
	listenerMu sync.RWMutex
	closed     bool
}

// tcp|http|host
func NewTunnelModeServer(process process, bridge NetBridge, task *file.Tunnel) *TunnelModeServer {
	s := new(TunnelModeServer)
	s.bridge = bridge
	s.process = process
	s.task = task
	return s
}

// 开始
func (s *TunnelModeServer) Start() error {
	if s == nil || s.task == nil {
		return errors.New("tunnel server is not configured")
	}
	s.listenerMu.RLock()
	closed := s.closed
	s.listenerMu.RUnlock()
	if closed {
		return net.ErrClosed
	}
	s.task.RLock()
	serverIP, port := s.task.ServerIp, s.task.Port
	s.task.RUnlock()
	listener, err := net.Listen("tcp", net.JoinHostPort(serverIP, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	s.listenerMu.Lock()
	if s.closed {
		s.listenerMu.Unlock()
		_ = listener.Close()
		return net.ErrClosed
	}
	s.listener = listener
	s.listenerMu.Unlock()
	conn.Accept(listener, func(c net.Conn) {
		// Protocol handlers take their own task snapshot and reserve the slot on
		// that exact client. Reserving here would allow an edit between this
		// callback and ProcessTunnel/ProcessHttp to charge different clients.
		s.process(conn.NewConn(c), s)
	})
	return nil
}

// close
func (s *TunnelModeServer) Close() error {
	if s == nil {
		return nil
	}
	s.listenerMu.Lock()
	s.closed = true
	listener := s.listener
	s.listener = nil
	s.listenerMu.Unlock()
	if listener == nil {
		return nil
	}
	return listener.Close()
}

// web管理方式
type WebServer struct {
	BaseServer
	lifecycleMu sync.Mutex
	server      *http.Server
	listener    net.Listener
	closed      bool
}

// 开始
func (s *WebServer) Start() error {
	if s == nil {
		return errors.New("web server is nil")
	}
	p, _ := beego.AppConfig.Int("web_port")
	if p == 0 {
		return errors.New("web management port is not configured")
	}
	beego.BConfig.WebConfig.Session.SessionOn = true
	// The web server below calls ServeTLS directly instead of Beego's HTTPS
	// runner. RegisterSession derives the Secure cookie attribute from this
	// flag, so it must be set before InitBeforeHTTPRun creates the manager.
	useTLS := beego.AppConfig.String("web_open_ssl") == "true"
	webIP := connection.WebManagerIP()
	if !useTLS && (connection.WebManagerUsesPortMultiplexing() || !isLoopbackWebAddress(webIP)) {
		logs.Warn("Web management panel is exposed over plaintext HTTP; use HTTPS or a loopback listener behind a TLS reverse proxy")
	}
	configureWebSessionTLS(useTLS)
	web.InitBeegoAssets()
	err := errors.New("Web management startup failure ")
	var l net.Listener
	if l, err = connection.GetWebManagerListener(); err == nil {
		beego.InitBeforeHTTPRun()
		server := newManagedHTTPServer(beego.BeeApp.Handlers)
		s.lifecycleMu.Lock()
		if s.closed {
			s.lifecycleMu.Unlock()
			_ = l.Close()
			return net.ErrClosed
		}
		s.listener = l
		s.server = server
		s.lifecycleMu.Unlock()
		if useTLS {
			keyPath := beego.AppConfig.String("web_key_file")
			certPath := beego.AppConfig.String("web_cert_file")
			err = server.ServeTLS(l, certPath, keyPath)
		} else {
			err = server.Serve(l)
		}
		s.lifecycleMu.Lock()
		if s.server == server {
			s.server = nil
			s.listener = nil
		}
		s.lifecycleMu.Unlock()
	} else {
		logs.Error(err)
	}
	return err
}

func configureWebSessionTLS(useTLS bool) {
	beego.BConfig.Listen.EnableHTTPS = useTLS
}

func newManagedHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		IdleTimeout:       httpIdleTimeout,
	}
}

func isLoopbackWebAddress(address string) bool {
	address = strings.TrimSpace(address)
	if strings.EqualFold(address, "localhost") {
		return true
	}
	ip := net.ParseIP(address)
	return ip != nil && ip.IsLoopback()
}

func (s *WebServer) Close() error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	if s.closed {
		s.lifecycleMu.Unlock()
		return nil
	}
	s.closed = true
	server, listener := s.server, s.listener
	s.server, s.listener = nil, nil
	s.lifecycleMu.Unlock()

	var closeErr error
	if server != nil {
		closeErr = server.Close()
	}
	if listener != nil {
		if err := listener.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

// new
func NewWebServer(bridge *bridge.Bridge) *WebServer {
	s := new(WebServer)
	s.bridge = bridge
	return s
}

type process func(c *conn.Conn, s *TunnelModeServer) error

// tcp proxy
func ProcessTunnel(c *conn.Conn, s *TunnelModeServer) error {
	if c == nil || s == nil || s.task == nil {
		if c != nil {
			_ = c.Close()
		}
		return errors.New("tunnel proxy server is not configured")
	}
	s.task.RLock()
	client, target := s.task.Client, s.task.Target
	taskID, taskPort := s.task.Id, s.task.Port
	s.task.RUnlock()
	if client == nil || target == nil {
		_ = c.Close()
		return errors.New("tunnel proxy client or target is not configured")
	}
	if err := s.CheckFlowAndConnNum(client); err != nil {
		_ = c.Close()
		return err
	}
	defer client.AddConn()
	targetAddr, err := target.GetRandomTarget()
	if err != nil {
		c.Close()
		client.RLock()
		clientID := client.Id
		client.RUnlock()
		logs.Warn("tcp port %d ,client id %d,task id %d connect error %s", taskPort, clientID, taskID, err.Error())
		return err
	}
	target.RLock()
	localProxy := target.LocalProxy
	target.RUnlock()
	return s.DealClient(c, client, targetAddr, nil, common.CONN_TCP, nil, nil, localProxy, s.task, nil)
}

// http proxy
func ProcessHttp(c *conn.Conn, s *TunnelModeServer) error {
	if c == nil || c.Conn == nil || s == nil || s.task == nil {
		if c != nil {
			_ = c.Close()
		}
		return errors.New("http proxy server is not configured")
	}
	if err := c.SetReadDeadline(time.Now().Add(httpProxyHandshakeTimeout)); err != nil {
		_ = c.Close()
		return err
	}

	_, addr, rb, err, r := c.GetHost()
	if err != nil {
		c.Close()
		logs.Info(err)
		return err
	}
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		_ = c.Close()
		return err
	}
	s.task.RLock()
	client, target := s.task.Client, s.task.Target
	s.task.RUnlock()
	clientConfig, ok := snapshotClientConfig(client)
	if !ok {
		_ = c.Close()
		return errors.New("http proxy client is not configured")
	}
	if err := s.proxyAuth(r, c, clientConfig.U, clientConfig.P); err != nil {
		return err
	}
	if err := s.CheckFlowAndConnNum(client); err != nil {
		_ = c.Close()
		return err
	}
	defer client.AddConn()
	if r.Method == "CONNECT" {
		if _, err := c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
			return err
		}
		rb = nil
	}
	localProxy := false
	if target != nil {
		target.RLock()
		localProxy = target.LocalProxy
		target.RUnlock()
	}
	return s.DealClient(c, client, addr, rb, common.CONN_TCP, nil, nil, localProxy, nil, nil)

}
