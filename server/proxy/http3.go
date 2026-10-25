package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// http3ListenerService owns the UDP socket and, when enabled, the QUIC
// listener used to multiplex browser HTTP/3 and Bridge QUIC sessions.
type http3ListenerService struct {
	packet net.PacketConn
	h3     *http3.Server
	ln     *quic.Listener
	tr     *quic.Transport
	once   sync.Once
	mu     sync.Mutex
	active map[*quic.Conn]struct{}
	wg     sync.WaitGroup
}

func (s *http3ListenerService) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	s.once.Do(func() {
		if s.ln != nil {
			if err := s.ln.Close(); err != nil && !http3ClosedError(err) && closeErr == nil {
				closeErr = err
			}
		}
		s.mu.Lock()
		for qc := range s.active {
			_ = qc.CloseWithError(0, "HTTP/3 server closed")
		}
		s.mu.Unlock()
		if s.h3 != nil {
			if err := s.h3.Close(); err != nil && !http3ClosedError(err) && closeErr == nil {
				closeErr = err
			}
		}
		if s.tr != nil {
			if err := s.tr.Close(); err != nil && !http3ClosedError(err) && closeErr == nil {
				closeErr = err
			}
		}
		s.wg.Wait()
		if s.packet != nil {
			if err := s.packet.Close(); err != nil && closeErr == nil && !errors.Is(err, net.ErrClosed) {
				closeErr = err
			}
		}
	})
	return closeErr
}

func http3ClosedError(err error) bool {
	return err == nil || errors.Is(err, net.ErrClosed) || errors.Is(err, http.ErrServerClosed) || errors.Is(err, quic.ErrServerClosed)
}

// startHTTP3 starts browser-facing HTTP/3. When bridge_http3=true, the same
// UDP socket also accepts the configured Bridge ALPN; ALPN and bridge_host are
// used to distinguish the two protocols before any HTTP handler runs.
func (s *httpServer) startHTTP3() (net.PacketConn, http3Service, error) {
	if s == nil || s.http3Port <= 0 {
		return nil, nil, errors.New("HTTP/3 端口未配置")
	}
	sharedBridge := beego.AppConfig.DefaultBool("bridge_http3", false) && s.bridgeServer != nil
	alpn := strings.TrimSpace(beego.AppConfig.String("quic_alpn"))
	if alpn == "" {
		alpn = "nps"
	}
	ip := strings.TrimSpace(beego.AppConfig.String("http_proxy_ip"))
	if ip == "" {
		ip = "0.0.0.0"
	}
	addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(ip, strconv.Itoa(s.http3Port)))
	if err != nil {
		return nil, nil, err
	}
	packetConn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, nil, err
	}
	tlsConfig := s.http3TLSConfig()
	service := &http3ListenerService{packet: packetConn, active: make(map[*quic.Conn]struct{})}

	if !sharedBridge {
		server := &http3.Server{Handler: http.HandlerFunc(s.handleHTTP3), TLSConfig: tlsConfig}
		service.h3 = server
		go func() {
			if serveErr := server.Serve(packetConn); serveErr != nil && !errors.Is(serveErr, net.ErrClosed) {
				logs.Error("HTTP/3 反代退出: %v", serveErr)
			}
		}()
		logs.Info("[HTTP/3] 域名反代监听 UDP %s", packetConn.LocalAddr())
		return packetConn, service, nil
	}

	// The HTTP/3 ALPN is first so ordinary browsers negotiate h3. As in the
	// shared Bridge/HTTP3 routing contract, SNI equal to bridge_host identifies
	// a Bridge session even when the peer negotiated h3.
	tlsConfig.NextProtos = bridgeHTTP3NextProtos(alpn)
	quicConfig := bridgeHTTP3QUICConfig()
	tr := &quic.Transport{Conn: packetConn}
	// Use the post-handshake listener. Besides disabling 0-RTT in the QUIC
	// config, this prevents the dispatcher from receiving a connection before
	// the client's TLS identity has been authenticated.
	ln, err := tr.Listen(tlsConfig, quicConfig)
	if err != nil {
		_ = packetConn.Close()
		return nil, nil, fmt.Errorf("监听 HTTP/3/Bridge QUIC 失败: %w", err)
	}
	service.tr, service.ln = tr, ln
	service.h3 = &http3.Server{Handler: http.HandlerFunc(s.handleHTTP3)}
	bridgeHost := strings.TrimSpace(beego.AppConfig.String("bridge_host"))
	go func() {
		for {
			qc, acceptErr := ln.Accept(context.Background())
			if acceptErr != nil {
				if !errors.Is(acceptErr, net.ErrClosed) && !errors.Is(acceptErr, quic.ErrServerClosed) {
					logs.Warn("HTTP/3/Bridge QUIC 接收失败: %v", acceptErr)
				}
				return
			}
			service.wg.Add(1)
			go func(qc *quic.Conn) {
				defer service.wg.Done()
				state := qc.ConnectionState().TLS
				if http3ConnectionIsBrowser(state.NegotiatedProtocol, state.ServerName, bridgeHost) {
					service.mu.Lock()
					service.active[qc] = struct{}{}
					service.mu.Unlock()
					defer func() {
						service.mu.Lock()
						delete(service.active, qc)
						service.mu.Unlock()
					}()
					if err := service.h3.ServeQUICConn(qc); err != nil && !errors.Is(err, net.ErrClosed) {
						logs.Debug("HTTP/3 连接结束: %v", err)
					}
					return
				}
				s.bridgeServer.ServeQUICConn(qc, 10*time.Second)
			}(qc)
		}
	}()
	logs.Info("[HTTP/3] 域名反代与 Bridge 共用 UDP %s，Bridge ALPN=%s，Host=%s", packetConn.LocalAddr(), alpn, bridgeHost)
	return packetConn, service, nil
}

func bridgeHTTP3QUICConfig() *quic.Config {
	quicConfig := &quic.Config{
		KeepAlivePeriod:    positiveSeconds(beego.AppConfig.DefaultInt("quic_keep_alive_period", 10)),
		MaxIdleTimeout:     positiveSeconds(beego.AppConfig.DefaultInt("quic_max_idle_timeout", 30)),
		MaxIncomingStreams: int64(beego.AppConfig.DefaultInt("quic_max_incoming_streams", 100000)),
		// HTTP requests are not guaranteed to be replay-safe. In particular,
		// allowing 0-RTT here could replay POST, JSON-RPC, or streaming AI
		// requests after QUIC session resumption.
		Allow0RTT: false,
	}
	if quicConfig.MaxIncomingStreams <= 0 {
		quicConfig.MaxIncomingStreams = 100000
	}
	return quicConfig
}

func http3ConnectionIsBrowser(alpn, serverName, bridgeHost string) bool {
	// When HTTP/3 and Bridge share the UDP listener, h3 is the browser ALPN
	// except when SNI explicitly selects the Bridge virtual host. Other ALPNs
	// belong to Bridge. This preserves the documented bridge_host override.
	return strings.TrimSpace(alpn) == http3.NextProtoH3 && !sameBridgeHost(serverName, bridgeHost)
}

func bridgeHTTP3NextProtos(alpn string) []string {
	protos := []string{http3.NextProtoH3}
	alpn = strings.TrimSpace(alpn)
	if alpn != "" && alpn != http3.NextProtoH3 {
		protos = append(protos, alpn)
	}
	return protos
}

func sameBridgeHost(serverName, bridgeHost string) bool {
	return normalizedBridgeHost(serverName) == normalizedBridgeHost(bridgeHost)
}

func normalizedBridgeHost(host string) string {
	host = strings.TrimSpace(host)
	if strings.Contains(host, "://") {
		if parsed, err := url.Parse(host); err == nil {
			host = parsed.Hostname()
		}
	} else if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

func positiveSeconds(value int) time.Duration {
	if value <= 0 {
		value = 30
	}
	return time.Duration(value) * time.Second
}

func (s *httpServer) handleHTTP3(w http.ResponseWriter, r *http.Request) {
	if r == nil {
		return
	}
	r.URL.Scheme = "https"
	s.handleTunneling(w, r)
}

func (s *httpServer) http3TLSConfig() *tls.Config {
	config := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{http3.NextProtoH3}}
	config.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if s != nil && s.acmeManager != nil {
			if cert, err := s.acmeManager.GetCertificate(hello); err == nil && cert != nil {
				return cert, nil
			}
		}
		return loadHTTP3Certificate(hello.ServerName)
	}
	return config
}

func loadHTTP3Certificate(serverName string) (*tls.Certificate, error) {
	serverName = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(serverName)), ".")
	if serverName == "" {
		return nil, errors.New("HTTP/3 请求缺少 SNI")
	}
	r := &http.Request{Host: serverName, RequestURI: "/", URL: &url.URL{Scheme: "https", Path: "/"}}
	if host, err := file.GetDb().GetInfoByHost(serverName, r); err == nil && host != nil {
		host.RLock()
		certFile, keyFile := host.CertFilePath, host.KeyFilePath
		justProxy, tlsOffload := host.HTTPSJustProxy, host.TLSOffload
		host.RUnlock()
		if justProxy || tlsOffload {
			return nil, errors.New("当前 Host 使用 TLS 原样透传/卸载，不支持 HTTP/3 七层反代")
		}
		if cert, err := loadCertificateMaterial(certFile, keyFile); err == nil {
			return cert, nil
		}
	}
	defaultCert := strings.TrimSpace(beego.AppConfig.String("https_default_cert_file"))
	defaultKey := strings.TrimSpace(beego.AppConfig.String("https_default_key_file"))
	if defaultCert == "" {
		defaultCert = filepath.Join(common.GetRunPath(), "conf", "server.pem")
	}
	if defaultKey == "" {
		defaultKey = filepath.Join(common.GetRunPath(), "conf", "server.key")
	}
	return loadCertificateMaterial(defaultCert, defaultKey)
}

func loadCertificateMaterial(certFile, keyFile string) (*tls.Certificate, error) {
	certFile, keyFile = strings.TrimSpace(certFile), strings.TrimSpace(keyFile)
	if certFile == "" || keyFile == "" {
		return nil, errors.New("证书或私钥未配置")
	}
	var certPEM, keyPEM []byte
	if strings.Contains(certFile, "-----BEGIN") || strings.Contains(keyFile, "-----BEGIN") {
		certPEM, keyPEM = []byte(certFile), []byte(keyFile)
	} else {
		certFile = common.ResolveConfigPath(certFile)
		keyFile = common.ResolveConfigPath(keyFile)
		var err error
		certPEM, err = os.ReadFile(certFile)
		if err != nil {
			return nil, err
		}
		keyPEM, err = os.ReadFile(keyFile)
		if err != nil {
			return nil, err
		}
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("加载证书失败: %w", err)
	}
	return &cert, nil
}
