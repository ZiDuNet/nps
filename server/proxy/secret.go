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
	client, target, flow, targetType := task.Client, task.Target, task.Flow, task.TargetType
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
	// explicitly enabled or the task's target type needs it; the default path
	// remains a zero-delay raw stream.
	if s.allowSecretLink || secretTaskNeedsLink(targetType) {
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
		// The task's target_type is authoritative. Legacy tasks (empty value)
		// keep TCP; "all" lets the visitor's Link select TCP or UDP per
		// connection, and a specific type forces that protocol.
		connType = secretTargetConnType(targetType, link.ConnType)
		if s.allowSecretLink {
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
		var err error
		if connType == common.CONN_UDP {
			// The visitor stream carries length-framed datagrams; the provider
			// receives the same stream and unframes it, so the server only has
			// to relay the bytes between the two sides.
			err = proxy.DealClientDatagram(wrapped, client, targetAddr, replay, connType, nil, flow, localProxy, task, nil)
		} else {
			err = proxy.DealClient(wrapped, client, targetAddr, replay, connType, nil, flow, localProxy, task, nil)
		}
		if err != nil {
			logs.Debug("secret connection failed: %v", err)
		}
	}()
	return nil
}

// secretTargetConnType resolves the protocol used for one Secret/P2P visitor
// connection. An empty task target type is the historical default of TCP.
func secretTargetConnType(taskType, linkType string) string {
	switch strings.ToLower(strings.TrimSpace(taskType)) {
	case common.CONN_UDP:
		return common.CONN_UDP
	case common.CONN_ALL:
		if strings.ToLower(strings.TrimSpace(linkType)) == common.CONN_UDP {
			return common.CONN_UDP
		}
		return common.CONN_TCP
	default:
		return common.CONN_TCP
	}
}

// secretTaskNeedsLink reports whether the task protocol requires the optional
// Secret Link metadata: UDP and mixed visitors must identify their datagram
// streams, while legacy TCP-only records keep the zero-delay raw path.
func secretTaskNeedsLink(taskType string) bool {
	switch strings.ToLower(strings.TrimSpace(taskType)) {
	case common.CONN_UDP, common.CONN_ALL:
		return true
	default:
		return false
	}
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
