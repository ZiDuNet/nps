package bridge

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/crypt"
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
	listener, err := net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return preparedWebSocketBridge{}, fmt.Errorf("listen WebSocket Bridge on %s:%d: %w", ip, port, err)
	}
	transport := "ws"
	if secure {
		transport = "wss"
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
			stream, streamErr := conn.NewWebSocketStream(ws)
			if streamErr != nil {
				_ = ws.Close()
				return
			}
			go func() {
				bridgeConn := conn.NewConn(stream)
				// The WebSocket connection is a reliable byte stream from the
				// Bridge protocol's perspective; it uses the ordinary TCP mux path.
				s.cliProcess(bridgeConn, "tcp")
			}()
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
