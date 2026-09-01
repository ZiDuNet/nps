package proxy

import (
	"errors"
	"net"
	"net/http"
	"sync"

	"ehang.io/nps/bridge"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"
)

type Service interface {
	Start() error
	Close() error
}

type NetBridge interface {
	SendLinkInfo(clientId int, link *conn.Link, t *file.Tunnel) (target net.Conn, err error)
}

// BaseServer struct
type BaseServer struct {
	id           int
	bridge       NetBridge
	task         *file.Tunnel
	errorContent []byte
	sync.Mutex
}

func NewBaseServer(bridge *bridge.Bridge, task *file.Tunnel) *BaseServer {
	return &BaseServer{
		bridge:       bridge,
		task:         task,
		errorContent: nil,
		Mutex:        sync.Mutex{},
	}
}

// add the flow
func (s *BaseServer) FlowAdd(in, out int64) {
	if s == nil || s.task == nil {
		return
	}
	s.task.RLock()
	flow := s.task.Flow
	s.task.RUnlock()
	if flow != nil {
		flow.Add(in, out)
	}
}

// change the flow
func (s *BaseServer) FlowAddHost(host *file.Host, in, out int64) {
	if host == nil {
		return
	}
	host.RLock()
	flow := host.Flow
	host.RUnlock()
	if flow != nil {
		flow.Add(in, out)
	}
}

// write fail bytes to the connection
func (s *BaseServer) writeConnFail(c net.Conn) {
	s.writeConnFailContent(c, s.errorContent)
}

// writeConnFailContent writes a request-specific failure body. HTTP proxy
// handlers may customize the body per request, so callers should pass their
// local snapshot instead of mutating BaseServer.errorContent.
func (s *BaseServer) writeConnFailContent(c net.Conn, content []byte) {
	c.Write([]byte(common.ConnectionFailBytes))
	if len(content) > 0 {
		_, _ = c.Write(content)
	}
}

// auth check for reverse-proxy hosts (401 + WWW-Authenticate).
func (s *BaseServer) auth(r *http.Request, c *conn.Conn, u, p string) error {
	return s.doAuth(r, c, u, p, common.UnauthorizedBytes, "401 Unauthorized")
}

// proxyAuth check for HTTP forward proxy (407 + Proxy-Authenticate).
func (s *BaseServer) proxyAuth(r *http.Request, c *conn.Conn, u, p string) error {
	return s.doAuth(r, c, u, p, common.ProxyAuthRequiredBytes, "407 Proxy Authentication Required")
}

func (s *BaseServer) doAuth(r *http.Request, c *conn.Conn, u, p, failBytes, errMsg string) error {
	if u != "" && p != "" && !common.CheckAuth(r, u, p) {
		c.Write([]byte(failBytes))
		c.Close()
		return errors.New(errMsg)
	}
	return nil
}

// check flow limit of the client ,and decrease the allow num of client
func (s *BaseServer) CheckFlowAndConnNum(client *file.Client) error {
	return checkClientConnection(client)
}

func checkClientConnection(client *file.Client) error {
	if client == nil {
		return errors.New("client is nil")
	}
	client.RLock()
	flow := client.Flow
	client.RUnlock()
	if flow != nil && flow.Exceeded() {
		return errors.New("Traffic exceeded")
	}
	if !client.GetConn() {
		return errors.New("Connections exceed the current client limit")
	}
	return nil
}

func in(target string, str_array []string) bool {
	for _, value := range str_array {
		if value == target {
			return true
		}
	}
	return false
}

// isIPWhiteBlocked returns whether a client must complete the IP allowlist
// challenge before a proxy protocol is allowed to proceed. Keep the snapshot
// under the client's lock so runtime console edits cannot race proxy workers.
func isIPWhiteBlocked(client *file.Client, remote string) bool {
	if client == nil {
		return false
	}
	client.RLock()
	enabled := client.IpWhite && client.IpWhitePass != ""
	verifyKey := client.VerifyKey
	allowlist := append([]string(nil), client.IpWhiteList...)
	client.RUnlock()
	return enabled && common.IsAuthIp(remote, verifyKey, allowlist)
}

// isClientBlackBlocked snapshots the mutable client policy before evaluating
// it. Proxy workers can run concurrently with console edits to the blacklist.
func isClientBlackBlocked(client *file.Client, remote string) bool {
	if client == nil {
		return false
	}
	client.RLock()
	verifyKey := client.VerifyKey
	blacklist := append([]string(nil), client.BlackIpList...)
	client.RUnlock()
	return common.IsBlackIp(remote, verifyKey, blacklist)
}

// snapshotClientConfig copies the small immutable-on-the-wire portion of a
// client configuration while holding the client's lock. Console updates may
// replace Cnf or mutate its fields while proxy workers are active.
func snapshotClientConfig(client *file.Client) (cfg file.Config, ok bool) {
	if client == nil {
		return cfg, false
	}
	client.RLock()
	if client.Cnf != nil {
		cfg = *client.Cnf
		ok = true
	}
	client.RUnlock()
	return cfg, ok
}

// snapshotHostProxyParts returns stable pointers selected from a host. The
// pointed-to Client/Target objects have their own synchronization for mutable
// fields and are intentionally not copied.
func snapshotHostProxyParts(host *file.Host) (client *file.Client, target *file.Target, flow *file.Flow, ok bool) {
	if host == nil {
		return nil, nil, nil, false
	}
	host.RLock()
	client, target, flow = host.Client, host.Target, host.Flow
	host.RUnlock()
	if client == nil || target == nil {
		return client, target, flow, false
	}
	_, ok = snapshotClientConfig(client)
	return client, target, flow, ok
}

// create a new connection and start bytes copying
func (s *BaseServer) DealClient(c *conn.Conn, client *file.Client, addr string,
	rb []byte, tp string, f func(), flow *file.Flow, localProxy bool, task *file.Tunnel, host *file.Host) error {
	if c == nil || c.Conn == nil {
		return errors.New("proxy connection is nil")
	}
	if client == nil {
		_ = c.Close()
		return errors.New("proxy client is nil")
	}
	client.RLock()
	clientID := client.Id
	clientConfig := client.Cnf
	if clientConfig != nil {
		configCopy := *clientConfig
		clientConfig = &configCopy
	}
	clientRate := client.Rate
	clientFlow := client.Flow
	client.RUnlock()
	if clientConfig == nil {
		_ = c.Close()
		return errors.New("proxy client configuration is nil")
	}
	if s == nil || s.bridge == nil {
		_ = c.Close()
		return errors.New("proxy bridge is nil")
	}
	if localProxy && !beego.AppConfig.DefaultBool("allow_local_proxy", false) {
		_ = c.Close()
		return errors.New("local proxy is disabled")
	}

	// 判断访问地址是否在全局黑名单内
	if IsGlobalBlackIp(c.RemoteAddr().String()) {
		c.Close()
		return nil
	}

	// 判断访问地址是否在黑名单内
	if isClientBlackBlocked(client, c.RemoteAddr().String()) {
		c.Close()
		return nil
	}

	// Enforce client IP allowlists at the shared hand-off point. Protocol
	// specific listeners may perform an earlier check to return a useful
	// challenge, but no unauthorized connection should reach the NPC bridge.
	if isIPWhiteBlocked(client, c.RemoteAddr().String()) {
		c.Close()
		return nil
	}

	protoVersion := ""
	if task != nil {
		task.RLock()
		protoVersion = task.ProtoVersion
		task.RUnlock()
	}

	link := conn.NewLink(tp, addr, clientConfig.Crypt, clientConfig.Compress, c.Conn.RemoteAddr().String(), localProxy, protoVersion)
	if s.bridge == nil {
		_ = c.Close()
		return errors.New("proxy bridge is nil")
	}
	var bridgeTask *file.Tunnel
	if s.task != nil {
		s.task.RLock()
		bridgeTask = &file.Tunnel{Mode: s.task.Mode}
		s.task.RUnlock()
	}
	if target, err := s.bridge.SendLinkInfo(clientID, link, bridgeTask); err != nil {
		logs.Warn("get connection from client id %d  error %s", clientID, err.Error())
		c.Close()
		return err
	} else {
		if target == nil {
			_ = c.Close()
			return errors.New("proxy bridge returned nil connection")
		}
		if f != nil {
			f()
		}
		if flow == nil {
			flow = clientFlow
		}
		conn.CopyWaitGroup(target, c.Conn, link.Crypt, link.Compress, clientRate, flow, true, rb, task, host)
	}
	return nil
}

// 判断访问地址是否在全局黑名单内
func IsGlobalBlackIp(ipPort string) bool {
	// 判断访问地址是否在全局黑名单内
	global := file.GetDb().GetGlobal()
	if global != nil {
		ip := common.GetIpByAddr(ipPort)
		global.RLock()
		blacklist := append([]string(nil), global.BlackIpList...)
		global.RUnlock()
		if in(ip, blacklist) {
			logs.Error("IP地址[" + ip + "]在全局黑名单列表内")
			return true
		}
	}

	return false
}
