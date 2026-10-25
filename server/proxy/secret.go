package proxy

import (
	"errors"
	"net"
	"strings"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"
)

// SecretServer handles the password-only proxy listener. The optional Link
// metadata is additive: old NPCs still send raw application bytes and those
// bytes are replayed after the bounded probe, so upgrading NPS never drops
// the first request byte.
type SecretServer struct {
	*BaseServer
	allowLocalProxy  bool
	allowSecretLink  bool
	allowSecretLocal bool
}

func NewSecretServer(bridge NetBridge, task *file.Tunnel, allowLocalProxy, allowSecretLink, allowSecretLocal bool) *SecretServer {
	return &SecretServer{
		BaseServer:       &BaseServer{bridge: bridge, task: task},
		allowLocalProxy:  allowLocalProxy,
		allowSecretLink:  allowSecretLink,
		allowSecretLocal: allowSecretLocal,
	}
}

// HandleSecret connects one visitor stream to the task target. It deliberately
// does not require a Link record: that is the compatibility boundary for all
// NPC releases up to 1.1.28.
func (s *SecretServer) HandleSecret(src net.Conn) error {
	if s == nil || s.BaseServer == nil || src == nil {
		return errors.New("secret server is not configured")
	}
	task := s.task
	if task == nil {
		_ = src.Close()
		return errors.New("secret task is nil")
	}
	task.RLock()
	client, target, flow := task.Client, task.Target, task.Flow
	task.RUnlock()
	if client == nil || target == nil {
		_ = src.Close()
		return errors.New("secret task target is incomplete")
	}
	if err := s.CheckFlowAndConnNum(client); err != nil {
		_ = src.Close()
		return err
	}

	var link *conn.Link
	var replay []byte
	// Legacy Secret clients send application bytes immediately after the
	// password. Only probe for the optional Link metadata when the feature is
	// explicitly enabled; the default path remains a zero-delay raw stream.
	if s.allowSecretLink {
		link, replay = conn.ProbeSecretLink(src)
	}
	targetAddr := ""
	if target != nil {
		targetAddr, _ = target.GetRandomTarget()
	}
	connType := common.CONN_TCP
	localProxy := false
	needAck := false
	if link != nil {
		needAck = link.Option.NeedAck
		if s.allowSecretLink {
			if requestedType := strings.ToLower(strings.TrimSpace(link.ConnType)); requestedType != "" && requestedType != common.CONN_TCP {
				client.AddConn()
				_ = src.Close()
				return errors.New("Secret Link currently supports TCP targets only")
			}
			if strings.TrimSpace(link.Host) != "" {
				targetAddr = strings.TrimSpace(link.Host)
			}
			localProxy = link.LocalProxy && s.allowLocalProxy && s.allowSecretLocal
		}
	}
	if targetAddr == "" {
		client.AddConn()
		_ = src.Close()
		return errors.New("secret target is empty")
	}
	// A Secret task's stored target is authoritative unless both the global
	// local-proxy switch and the explicit Secret local-proxy flag allow a
	// caller-supplied local target.
	if !localProxy {
		target.RLock()
		storedLocalProxy := target.LocalProxy
		target.RUnlock()
		localProxy = storedLocalProxy && s.allowLocalProxy && s.allowSecretLocal
	}
	if !localProxy && !s.allowSecretLink {
		// The target selected above is already the stored target when Link access
		// is disabled. Keep this branch explicit for future protocol extensions.
		connType = common.CONN_TCP
	}
	if link != nil && needAck {
		if err := conn.WriteACK(src, link.Option.Timeout); err != nil {
			client.AddConn()
			_ = src.Close()
			return err
		}
	}
	proxy := &BaseServer{bridge: s.bridge, task: task}
	go func() {
		defer client.AddConn()
		wrapped := conn.NewConn(src)
		if err := proxy.DealClient(wrapped, client, targetAddr, replay, connType, nil, flow, localProxy, task, nil); err != nil {
			logs.Debug("secret connection failed: %v", err)
		}
	}()
	return nil
}

// SecretConfig centralizes the defaults used by runtime task startup.
// Keeping these reads in one place makes the Web-configured safety switches
// apply equally to persisted and dynamically-created Secret tasks.
func SecretConfig() (allowLocalProxy, allowLink, allowLocal bool) {
	allowLocalProxy = beego.AppConfig.DefaultBool("allow_local_proxy", false)
	allowLink = beego.AppConfig.DefaultBool("allow_secret_link", false)
	allowLocal = beego.AppConfig.DefaultBool("allow_secret_local", false)
	return
}
