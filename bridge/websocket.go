package bridge

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/server/connection"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"
	"github.com/gorilla/websocket"
)

type preparedWebSocketBridge struct {
	server    *http.Server
	listener  net.Listener
	port      int
	path      string
	transport string
}

func (s *Bridge) prepareWebSocketBridge(port int, secure bool) (preparedWebSocketBridge, error) {
	path := strings.TrimSpace(beego.AppConfig.String("bridge_path"))
	if path == "" {
		path = "/ws"
	}
	parsedPath, err := url.ParseRequestURI(path)
	if err != nil || !strings.HasPrefix(path, "/") || parsedPath.RawQuery != "" || parsedPath.Fragment != "" {
		return preparedWebSocketBridge{}, fmt.Errorf("invalid bridge_path %q: expected an absolute URL path", path)
	}
	ip := strings.TrimSpace(beego.AppConfig.String("bridge_ip"))
	if ip == "" {
		ip = "0.0.0.0"
	}
	var listener net.Listener
	if secure {
		listener, err = connection.GetBridgeWSSListener(port)
	} else {
		listener, err = connection.GetBridgeWSListener(port)
	}
	if err != nil {
		return preparedWebSocketBridge{}, fmt.Errorf("listen WebSocket Bridge on %s:%d: %w", ip, port, err)
	}
	transport := "ws"
	virtualListener := s.virtualWS
	if secure {
		transport = "wss"
		virtualListener = s.virtualWSS
		listener = tls.NewListener(listener, &tls.Config{
			Certificates: []tls.Certificate{crypt.GetCert()},
			MinVersion:   tls.VersionTLS12,
		})
	}
	upgrader := websocket.Upgrader{
		HandshakeTimeout: 10 * time.Second,
		ReadBufferSize:   4096,
		WriteBufferSize:  4096,
	}
	server := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    16 << 10,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != path {
				http.NotFound(w, r)
				return
			}
			ws, upgradeErr := upgrader.Upgrade(w, r, nil)
			if upgradeErr != nil {
				logs.Trace("[Bridge] %s WebSocket upgrade rejected: %v", transport, upgradeErr)
				return
			}
			stream, streamErr := conn.NewWebSocketStreamWithRemote(ws, websocketRemoteAddr(r))
			if streamErr != nil {
				_ = ws.Close()
				return
			}
			// The WebSocket stream enters the same in-process Bridge accept path
			// as bridge://ws / bridge://wss targets.
			if virtualListener == nil || !virtualListener.Serve(stream) {
				_ = stream.Close()
			}
		}),
	}
	return preparedWebSocketBridge{
		server:    server,
		listener:  listener,
		port:      port,
		path:      path,
		transport: transport,
	}, nil
}

// websocketRemoteAddr returns a forwarded client address only when the TCP
// peer is explicitly trusted. This keeps the Bridge's IP allow/deny and audit
// checks safe behind a reverse proxy without trusting arbitrary browser
// supplied headers on direct connections.
func websocketRemoteAddr(r *http.Request) net.Addr {
	if r == nil {
		return nil
	}
	header := strings.TrimSpace(beego.AppConfig.String("bridge_real_ip_header"))
	if header == "" || !trustedBridgeProxy(r.RemoteAddr) {
		return nil
	}
	raw := strings.TrimSpace(r.Header.Get(header))
	if strings.EqualFold(header, "X-Forwarded-For") && strings.Contains(raw, ",") {
		raw = strings.TrimSpace(strings.Split(raw, ",")[0])
	}
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		return nil
	}
	return &net.TCPAddr{IP: ip}
}

func trustedBridgeProxy(remote string) bool {
	peer := remote
	if host, _, err := net.SplitHostPort(strings.TrimSpace(remote)); err == nil {
		peer = host
	}
	ip := net.ParseIP(strings.TrimSpace(peer))
	if ip == nil {
		return false
	}
	configured := strings.TrimSpace(beego.AppConfig.String("bridge_trusted_ips"))
	if configured == "" {
		return false
	}
	for _, item := range strings.FieldsFunc(configured, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t' }) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.Contains(item, "/") {
			if _, network, err := net.ParseCIDR(item); err == nil && network.Contains(ip) {
				return true
			}
			continue
		}
		if trusted := net.ParseIP(item); trusted != nil && trusted.Equal(ip) {
			return true
		}
	}
	return false
}
