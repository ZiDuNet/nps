//go:build !npcgui
// +build !npcgui

package proxy

import (
	"bytes"
	"errors"
	"io"
	"net"
	"time"

	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego/logs"
)

// ProcessMix recognizes HTTP proxy and SOCKS5 handshakes on one TCP listener.
// The existing dedicated proxy modes remain available and retain their
// original behavior; this handler is used only for the additive mixProxy mode.
func ProcessMix(c *conn.Conn, server *TunnelModeServer) error {
	if c == nil || c.Conn == nil || server == nil || server.task == nil {
		if c != nil {
			_ = c.Close()
		}
		return errors.New("mixed proxy server is not configured")
	}

	task := server.task
	task.RLock()
	client := task.Client
	registeredOnly := task.RegisteredOnly
	task.RUnlock()
	httpEnabled, socksEnabled := effectiveMixProxyFlags(task)
	if client == nil {
		_ = c.Close()
		return errors.New("mixed proxy client is not configured")
	}

	remote := c.Conn.RemoteAddr()
	if remote == nil || SourceDenied(remote, client, &task.SourceLists, taskName(task)) || isIPWhiteBlocked(client, remote.String()) || isRegisteredOnlyBlocked(client, remote.String(), registeredOnly) {
		_ = c.Close()
		return nil
	}

	if err := c.SetReadDeadline(time.Now().Add(socks5HandshakeTimeout)); err != nil {
		_ = c.Close()
		return err
	}
	var prefix [2]byte
	if _, err := io.ReadFull(c, prefix[:]); err != nil {
		_ = c.Close()
		return err
	}

	if prefix[0] == 5 {
		if !socksEnabled {
			_, _ = c.Write([]byte{5, 0xff})
			_ = c.Close()
			logMixProxyProtocolDisabled("SOCKS5", task, remote)
			return errors.New("SOCKS5 proxy is disabled")
		}
		proxy := NewSock5ModeServer(server.bridge, task)
		proxy.handleConn(&mixPrefixedConn{Conn: c.Conn, prefix: bytes.NewReader(prefix[:])})
		return nil
	}

	if !looksLikeHTTPProxyRequest(prefix[:]) {
		_ = c.Close()
		return errors.New("unknown mixed proxy protocol")
	}
	if !httpEnabled {
		_, _ = c.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"))
		_ = c.Close()
		logMixProxyProtocolDisabled("HTTP", task, remote)
		return errors.New("HTTP proxy is disabled")
	}
	_ = c.SetReadDeadline(time.Time{})
	c.Rb = append(prefix[:], c.Rb...)
	return ProcessHttp(c, server)
}

type mixPrefixedConn struct {
	net.Conn
	prefix *bytes.Reader
}

func (c *mixPrefixedConn) Read(p []byte) (int, error) {
	if c.prefix != nil && c.prefix.Len() > 0 {
		return c.prefix.Read(p)
	}
	return c.Conn.Read(p)
}

func looksLikeHTTPProxyRequest(prefix []byte) bool {
	switch string(prefix) {
	case "GE", "PO", "HE", "PU", "DE", "OP", "CO", "TR", "PA", "PR", "MK", "MO", "LO", "UN", "RE", "AC", "SE", "LI":
		return true
	default:
		return false
	}
}

func effectiveMixProxyFlags(task *file.Tunnel) (httpEnabled, socksEnabled bool) {
	if task == nil {
		return false, false
	}
	task.RLock()
	defer task.RUnlock()
	switch task.Mode {
	case "httpProxy":
		return true, false
	case "socks5":
		return false, true
	case "mixProxy":
		return task.HttpProxy, task.Socks5Proxy
	default:
		return false, false
	}
}

func logMixProxyProtocolDisabled(protocol string, task *file.Tunnel, remote net.Addr) {
	if task == nil {
		return
	}
	task.RLock()
	id := task.Id
	task.RUnlock()
	logs.Warn("mixed proxy tunnel %d rejected disabled %s request from %v", id, protocol, remote)
}
