package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/server/tool"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"
)

// IPRegistrationServer is an optional, isolated listener for the public IP
// registration page. It serves only the registration handler, not the Web
// management routes.
type IPRegistrationServer struct {
	server          *http.Server
	listener        net.Listener
	virtualServer   *http.Server
	virtualListener *tool.VirtualListener
}

// StartIPRegistrationServer starts a separate registration listener when
// ip_register_port is configured. A port of 0 (the default) leaves the
// existing management-panel /ip/register endpoint as the only entry point.
func StartIPRegistrationServer(handler http.Handler) (*IPRegistrationServer, error) {
	port := beego.AppConfig.DefaultInt("ip_register_port", 0)
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("ip_register_port must be between 1 and 65535, got %d", port)
	}
	if handler == nil {
		if port == 0 {
			return nil, nil
		}
		return nil, errors.New("IP registration handler is nil")
	}

	bindIP := strings.TrimSpace(beego.AppConfig.String("ip_register_ip"))
	if bindIP == "" {
		// The optional listener is intentionally loopback-only unless the
		// operator explicitly chooses a public or private interface.
		bindIP = "127.0.0.1"
	}

	certPath := strings.TrimSpace(beego.AppConfig.String("ip_register_cert_file"))
	keyPath := strings.TrimSpace(beego.AppConfig.String("ip_register_key_file"))
	if (certPath == "") != (keyPath == "") {
		return nil, errors.New("ip_register_cert_file and ip_register_key_file must both be configured for TLS")
	}
	var tlsConfig *tls.Config
	if certPath != "" {
		cert, err := tls.LoadX509KeyPair(resolveIPRegisterPath(certPath), resolveIPRegisterPath(keyPath))
		if err != nil {
			return nil, fmt.Errorf("load IP registration TLS certificate: %w", err)
		}
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	}

	var listener net.Listener
	var err error
	if port > 0 {
		address := net.JoinHostPort(bindIP, strconv.Itoa(port))
		listener, err = net.Listen("tcp", address)
		if err != nil {
			return nil, fmt.Errorf("listen for IP registration on %s: %w", address, err)
		}
		if tlsConfig != nil {
			listener = tls.NewListener(listener, tlsConfig)
		}
	}
	newHTTPServer := func() *http.Server {
		return &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
	}
	virtual := tool.NewVirtualListener(nil)
	virtualServer := newHTTPServer()
	registrationServer := &IPRegistrationServer{virtualServer: virtualServer, virtualListener: virtual}
	tool.SetIPRegisterListener(virtual)
	go func() {
		if err := virtualServer.Serve(virtual); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			logs.Error("in-process IP registration listener stopped: %v", err)
		}
	}()
	if port == 0 {
		return registrationServer, nil
	}
	srv := newHTTPServer()
	registrationServer.server, registrationServer.listener = srv, listener
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logs.Error("IP registration listener stopped: %v", err)
		}
	}()

	scheme := "http"
	if tlsConfig != nil {
		scheme = "https"
	}
	logs.Info("IP registration page listening on %s://%s", scheme, listener.Addr())
	return registrationServer, nil
}

func resolveIPRegisterPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(common.GetRunPath(), path)
}

// Close gracefully stops the optional registration listener.
func (s *IPRegistrationServer) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.server.Shutdown(ctx); err != nil {
			closeErr = err
			if s.listener != nil {
				_ = s.listener.Close()
			}
		}
	}
	if s.virtualServer != nil {
		if err := s.virtualServer.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	if s.virtualListener != nil {
		tool.SetIPRegisterListener(nil)
		if err := s.virtualListener.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}
