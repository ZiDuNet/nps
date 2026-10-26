//go:build !npcgui
// +build !npcgui

package proxy

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/bridge"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/instancekey"
	"ehang.io/nps/server/connection"
	"ehang.io/nps/server/tool"
	"ehang.io/nps/server/traffic"
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
	virtual    map[net.Conn]struct{}
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
	virtual := make([]net.Conn, 0, len(s.virtual))
	for c := range s.virtual {
		virtual = append(virtual, c)
		delete(s.virtual, c)
	}
	s.listenerMu.Unlock()
	for _, c := range virtual {
		_ = c.Close()
	}
	if listener == nil {
		return nil
	}
	return listener.Close()
}

// DialVirtual injects a connection into this tunnel's normal processing path
// without opening a second public TCP socket. The target sees the original
// visitor address, so source ACLs, logging, and PROXY-protocol generation keep
// their usual semantics for nested tunnel routing.
func (s *TunnelModeServer) DialVirtual(remote string) (net.Conn, error) {
	if s == nil || s.task == nil || s.process == nil {
		return nil, errors.New("target tunnel is not configured")
	}
	remoteAddr, err := net.ResolveTCPAddr("tcp", strings.TrimSpace(remote))
	if err != nil {
		return nil, errors.New("invalid virtual visitor address")
	}
	clientConn, serverConn := net.Pipe()
	s.listenerMu.Lock()
	listener := s.listener
	if s.closed || listener == nil {
		s.listenerMu.Unlock()
		_ = clientConn.Close()
		_ = serverConn.Close()
		return nil, errors.New("target tunnel is not running")
	}
	if s.virtual == nil {
		s.virtual = make(map[net.Conn]struct{})
	}
	s.virtual[serverConn] = struct{}{}
	s.listenerMu.Unlock()

	wrapped := &virtualAddressConn{Conn: serverConn, local: listener.Addr(), remote: remoteAddr}
	go func() {
		defer func() {
			s.listenerMu.Lock()
			delete(s.virtual, serverConn)
			s.listenerMu.Unlock()
			_ = serverConn.Close()
		}()
		_ = s.process(conn.NewConn(wrapped), s)
	}()
	return clientConn, nil
}

type virtualAddressConn struct {
	net.Conn
	local  net.Addr
	remote net.Addr
}

func (c *virtualAddressConn) LocalAddr() net.Addr  { return c.local }
func (c *virtualAddressConn) RemoteAddr() net.Addr { return c.remote }

// web管理方式
type WebServer struct {
	BaseServer
	lifecycleMu   sync.Mutex
	server        *http.Server
	virtualServer *http.Server
	listener      net.Listener
	virtual       *tool.VirtualListener
	closed        bool
}

// 开始
func (s *WebServer) Start() error {
	if s == nil {
		return errors.New("web server is nil")
	}
	p, _ := beego.AppConfig.Int("web_port")
	if p < 0 || p > 65535 {
		return fmt.Errorf("web management port is invalid: %d", p)
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
	if err := configureWebSessionSecurity(useTLS); err != nil {
		return err
	}
	web.InitBeegoAssets()
	beego.InitBeforeHTTPRun()
	handler := beego.BeeApp.Handlers
	virtual := tool.NewVirtualListener(nil)
	virtualServer := newManagedHTTPServer(handler)
	var listener net.Listener
	var err error
	if p > 0 {
		listener, err = connection.GetWebManagerListener()
		if err != nil {
			_ = virtual.Close()
			return fmt.Errorf("start Web management listener: %w", err)
		}
	}
	var server *http.Server
	if listener != nil {
		server = newManagedHTTPServer(handler)
	}
	s.lifecycleMu.Lock()
	if s.closed {
		s.lifecycleMu.Unlock()
		_ = virtual.Close()
		if listener != nil {
			_ = listener.Close()
		}
		return net.ErrClosed
	}
	s.listener, s.server = listener, server
	s.virtual, s.virtualServer = virtual, virtualServer
	tool.SetWebServerListener(virtual)
	s.lifecycleMu.Unlock()
	go func() {
		if serveErr := virtualServer.Serve(virtual); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && !errors.Is(serveErr, net.ErrClosed) {
			logs.Error("in-process Web management listener stopped: %v", serveErr)
		}
	}()
	if listener == nil {
		logs.Info("web_port=0: management panel is available only through the in-process Bridge route")
		return nil
	}
	if useTLS {
		keyPath := resolveWebTLSPath(beego.AppConfig.String("web_key_file"))
		certPath := resolveWebTLSPath(beego.AppConfig.String("web_cert_file"))
		err = server.ServeTLS(listener, certPath, keyPath)
	} else {
		err = server.Serve(listener)
	}
	s.lifecycleMu.Lock()
	if s.server == server {
		s.server = nil
		s.listener = nil
	}
	s.lifecycleMu.Unlock()
	return err
}

func resolveWebTLSPath(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(common.GetRunPath(), path)
}

func configureWebSessionTLS(useTLS bool) {
	beego.BConfig.Listen.EnableHTTPS = useTLS
}

// configureWebSessionSecurity switches the panel to Beego's encrypted cookie
// provider and derives its keys from an installation-local instance key. The
// default memory provider uses an unsigned session identifier, which lets an
// attacker forge the identifier after learning the process's session state.
// Existing deployments can keep an explicit sessionConfig for compatibility;
// otherwise the key is created under conf/instance.key and survives restarts.
func configureWebSessionSecurity(useTLS bool) error {
	if strings.TrimSpace(beego.AppConfig.String("sessionConfig")) != "" {
		return nil
	}
	keyPath := strings.TrimSpace(beego.AppConfig.String("instance_key_file"))
	if keyPath == "" {
		keyPath = filepath.Join(common.GetRunPath(), "conf", "instance.key")
	} else if !filepath.IsAbs(keyPath) {
		keyPath = filepath.Join(common.GetRunPath(), keyPath)
	}
	key, err := instancekey.LoadOrCreate(keyPath)
	if err != nil {
		return err
	}
	securityKey, err := key.DeriveKey("web-session-signing", 32)
	if err != nil {
		return err
	}
	// Beego's cookie provider consumes blockKey as the literal AES key rather
	// than decoding it from hex. Keep it printable for JSON while using a valid
	// AES-256 length; encoding 32 raw bytes as hex would produce a 64-byte key
	// and panic during InitBeforeHTTPRun.
	blockKey, err := key.DeriveKey("web-session-encryption", 16)
	if err != nil {
		return err
	}
	securityName, err := key.DeriveKey("web-session-name", 16)
	if err != nil {
		return err
	}
	providerConfig, err := json.Marshal(map[string]interface{}{
		"securityKey":  hex.EncodeToString(securityKey),
		"blockKey":     hex.EncodeToString(blockKey),
		"securityName": hex.EncodeToString(securityName),
		"cookieName":   "nps_session",
		"secure":       useTLS,
	})
	if err != nil {
		return err
	}
	managerConfig, err := json.Marshal(map[string]interface{}{
		"cookieName": "nps_session",
		// SessionStart otherwise emits a temporary unsigned-looking session id
		// before the cookie provider serializes the encrypted state. That creates
		// duplicate Set-Cookie headers and some reverse proxies keep the first
		// value, losing the login nonce. The deferred/manual SessionRelease calls
		// below are responsible for the single encrypted cookie.
		"enableSetCookie": false,
		"gclifetime":      beego.BConfig.WebConfig.Session.SessionGCMaxLifetime,
		"maxLifetime":     beego.BConfig.WebConfig.Session.SessionGCMaxLifetime,
		"secure":          useTLS,
		"cookieLifeTime":  beego.BConfig.WebConfig.Session.SessionCookieLifeTime,
		"providerConfig":  string(providerConfig),
	})
	if err != nil {
		return err
	}
	if err := beego.AppConfig.Set("sessionConfig", string(managerConfig)); err != nil {
		return err
	}
	beego.BConfig.WebConfig.Session.SessionProvider = "cookie"
	return nil
}

func newManagedHTTPServer(handler http.Handler) *http.Server {
	if beego.AppConfig.DefaultBool("web_close_on_not_found", false) {
		handler = closeUnknownWebPaths(handler)
	}
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		IdleTimeout:       httpIdleTimeout,
	}
}

// closeUnknownWebPaths keeps the optional hardening behavior local to the
// management server. Beego still renders normal errors when disabled; when
// enabled, a 404 is discarded and an HTTP/1.x connection is closed whenever
// the server supports Hijack. HTTP/2 clients receive the regular 404 because
// the protocol has no connection-level hijack primitive.
func closeUnknownWebPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture := &notFoundCapture{ResponseWriter: w}
		next.ServeHTTP(capture, r)
		capture.flush()
	})
}

type notFoundCapture struct {
	http.ResponseWriter
	status    int
	committed bool
	body      bytes.Buffer
}

func (w *notFoundCapture) WriteHeader(status int) {
	if w.committed {
		return
	}
	w.status = status
}

func (w *notFoundCapture) Write(data []byte) (int, error) {
	if w.committed {
		return w.ResponseWriter.Write(data)
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(data)
}

func (w *notFoundCapture) Flush() {
	w.flushToUnderlying()
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *notFoundCapture) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("management response does not support hijacking")
	}
	w.committed = true
	return hijacker.Hijack()
}

func (w *notFoundCapture) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *notFoundCapture) flush() {
	if w.committed {
		return
	}
	if w.status == http.StatusNotFound {
		if hijacker, ok := w.ResponseWriter.(http.Hijacker); ok {
			if connection, _, err := hijacker.Hijack(); err == nil {
				_ = connection.Close()
				w.committed = true
				return
			}
		}
	}
	w.flushToUnderlying()
}

func (w *notFoundCapture) flushToUnderlying() {
	if w.committed {
		return
	}
	w.committed = true
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	w.ResponseWriter.WriteHeader(status)
	if w.body.Len() > 0 {
		_, _ = w.ResponseWriter.Write(w.body.Bytes())
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
	server, virtualServer, listener, virtual := s.server, s.virtualServer, s.listener, s.virtual
	s.server, s.virtualServer, s.listener, s.virtual = nil, nil, nil, nil
	tool.SetWebServerListener(nil)
	s.lifecycleMu.Unlock()

	var closeErr error
	if server != nil {
		closeErr = server.Close()
	}
	if virtualServer != nil {
		if err := virtualServer.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	if virtual != nil {
		if err := virtual.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
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
	taskID, taskPort, taskMode := s.task.Id, s.task.Port, s.task.Mode
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
	if taskMode == "tcp" {
		if err := s.task.TryAcquireConn(); err != nil {
			_ = c.Close()
			return err
		}
		defer s.task.ReleaseConn()
	}
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
	resource := traffic.Resource{Kind: "tunnel", ID: taskID}
	started := time.Now()
	requestID := traffic.NewRequestID()
	traffic.Publish(traffic.Event{
		Type:         "connection_start",
		Time:         started.UTC(),
		Resource:     resource,
		RequestID:    requestID,
		RemoteAddr:   c.RemoteAddr().String(),
		TargetAddr:   targetAddr,
		ListenerAddr: c.LocalAddr().String(),
	})
	err = s.DealClient(c, client, targetAddr, nil, common.CONN_TCP, nil, nil, localProxy, s.task, nil)
	end := traffic.Event{
		Type:       "connection_end",
		Time:       time.Now().UTC(),
		Resource:   resource,
		RequestID:  requestID,
		RemoteAddr: c.RemoteAddr().String(),
		TargetAddr: targetAddr,
		DurationMS: time.Since(started).Milliseconds(),
		Complete:   err == nil,
	}
	if err != nil {
		end.Error = err.Error()
	}
	traffic.Publish(end)
	return err
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
	if !mixProxyDestinationAllowed(s.task, addr) {
		_, _ = c.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"))
		_ = c.Close()
		return errors.New("destination denied by mixed proxy ACL")
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
	resource := traffic.Resource{Kind: "tunnel", ID: s.task.Id}
	requestID := traffic.NewRequestID()
	started := time.Now()
	traffic.Publish(traffic.Event{
		Type:         "request_start",
		Time:         started.UTC(),
		Resource:     resource,
		RequestID:    requestID,
		Method:       r.Method,
		Host:         r.Host,
		Path:         r.URL.RequestURI(),
		RemoteAddr:   c.RemoteAddr().String(),
		ListenerAddr: c.LocalAddr().String(),
		TargetAddr:   addr,
		ContentType:  r.Header.Get("Content-Type"),
		Headers:      traffic.SafeHeaders(r.Header),
	})
	err = s.DealClient(c, client, addr, rb, common.CONN_TCP, nil, nil, localProxy, s.task, nil)
	end := traffic.Event{
		Type:       "connection_end",
		Time:       time.Now().UTC(),
		Resource:   resource,
		RequestID:  requestID,
		TargetAddr: addr,
		DurationMS: time.Since(started).Milliseconds(),
		Complete:   err == nil,
	}
	if err != nil {
		end.Error = err.Error()
	}
	traffic.Publish(end)
	return err

}
