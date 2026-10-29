package proxy

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/bridge"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/connectionlog"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/ipreg"
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

func (s *BaseServer) FlowAddInbound(bytes int64) {
	s.FlowAdd(bytes, 0)
}

func (s *BaseServer) FlowAddOutbound(bytes int64) {
	s.FlowAdd(0, bytes)
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

func (s *BaseServer) FlowAddHostInbound(host *file.Host, bytes int64) {
	s.FlowAddHost(host, bytes, 0)
}

func (s *BaseServer) FlowAddHostOutbound(host *file.Host, bytes int64) {
	s.FlowAddHost(host, 0, bytes)
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

// hostBasicAuth snapshots the optional host-level account list. A non-empty
// value is considered configured even when legacy data is malformed; such a
// rule must fail closed instead of silently falling back to client auth.
func hostBasicAuth(host *file.Host) (value string, configured bool) {
	if host == nil {
		return "", false
	}
	host.RLock()
	value = strings.TrimSpace(host.Auth)
	host.RUnlock()
	return value, value != ""
}

// authHostRequest applies the host-level Basic account list when present and
// falls back to the historical client-level account otherwise. It is used by
// hijacked HTTP connections where the failure response must be written on the
// raw socket.
func (s *BaseServer) authHostRequest(r *http.Request, c *conn.Conn, host *file.Host, u, p string) error {
	if authValue, configured := hostBasicAuth(host); configured {
		if file.CheckBasicAuth(r, authValue) {
			return nil
		}
		if c != nil {
			c.Write([]byte(common.UnauthorizedBytes))
			_ = c.Close()
		}
		return errors.New("401 Unauthorized")
	}
	return s.auth(r, c, u, p)
}

// proxyAuth check for HTTP forward proxy (407 + Proxy-Authenticate).
func (s *BaseServer) proxyAuth(r *http.Request, c *conn.Conn, u, p string) error {
	if s != nil && s.task != nil {
		s.task.RLock()
		authValue := strings.TrimSpace(s.task.ProxyAuth)
		s.task.RUnlock()
		if authValue != "" && !file.CheckBasicAuth(r, authValue) {
			if c != nil {
				_, _ = c.Write([]byte(common.ProxyAuthRequiredBytes))
				_ = c.Close()
			}
			return errors.New("407 Proxy Authentication Required")
		}
		if authValue != "" {
			return nil
		}
	}
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

// isRegisteredOnlyBlocked enforces the short-lived IP registration policy.
func isRegisteredOnlyBlocked(client *file.Client, remote string, required bool) bool {
	if client == nil {
		return required
	}
	client.RLock()
	clientID := client.Id
	required = required || client.RegisteredOnly
	client.RUnlock()
	if !required {
		return false
	}
	return !ipreg.RegisteredString(clientID, remote)
}

func isHostRegisteredOnlyBlocked(client *file.Client, host *file.Host, remote string) bool {
	if host == nil {
		return false
	}
	host.RLock()
	required := host.RegisteredOnly
	host.RUnlock()
	return isRegisteredOnlyBlocked(client, remote, required)
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
	return s.dealClient(c, client, addr, rb, tp, f, flow, localProxy, task, host, false)
}

// DealClientDatagram is DealClient for a UDP link whose payload is a stream
// of length-framed datagrams. The flag travels with the Link so the provider
// knows to preserve datagram boundaries instead of copying a raw byte stream.
func (s *BaseServer) DealClientDatagram(c *conn.Conn, client *file.Client, addr string,
	rb []byte, tp string, f func(), flow *file.Flow, localProxy bool, task *file.Tunnel, host *file.Host) error {
	return s.dealClient(c, client, addr, rb, tp, f, flow, localProxy, task, host, true)
}

func (s *BaseServer) dealClient(c *conn.Conn, client *file.Client, addr string,
	rb []byte, tp string, f func(), flow *file.Flow, localProxy bool, task *file.Tunnel, host *file.Host, datagramFramed bool) (err error) {
	if c == nil || c.Conn == nil {
		return errors.New("proxy connection is nil")
	}
	startedAt := time.Now()
	logResult, logError := connectionlog.ResultSuccess, ""
	var bytesIn, bytesOut int64
	logEvent := proxyConnectionLogEvent(client, task, host, tp, addr,
		proxyAddrString(c.Conn.RemoteAddr()), proxyAddrString(c.Conn.LocalAddr()), startedAt)
	defer func() {
		endedAt := time.Now()
		logEvent.Time = endedAt.UTC()
		logEvent.EndedAt = endedAt.UTC()
		logEvent.DurationMS = endedAt.Sub(startedAt).Milliseconds()
		logEvent.BytesIn = bytesIn
		logEvent.BytesOut = bytesOut
		logEvent.Result = logResult
		logEvent.Error = logError
		if err != nil {
			logEvent.Result = connectionlog.ResultFailure
			logEvent.Error = err.Error()
		}
		if appendErr := connectionlog.Append(logEvent); appendErr != nil {
			logs.Warn("写入连接日志失败: %v", appendErr)
		}
	}()
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
	if localProxy && !localProxyAllowedForClient(client, task) {
		_ = c.Close()
		return errors.New("local proxy is disabled")
	}
	if task != nil && SourceDenied(c.Conn.RemoteAddr(), client, &task.SourceLists, taskName(task)) {
		logResult, logError = connectionlog.ResultFailure, "source denied by tunnel access policy"
		_ = c.Close()
		return nil
	}

	// 判断访问地址是否在全局黑名单内
	if IsGlobalBlackIp(c.RemoteAddr().String()) {
		logResult, logError = connectionlog.ResultFailure, "source denied by global access policy"
		c.Close()
		return nil
	}

	// 判断访问地址是否在黑名单内
	if isClientBlackBlocked(client, c.RemoteAddr().String()) {
		logResult, logError = connectionlog.ResultFailure, "source denied by client access policy"
		c.Close()
		return nil
	}

	protoVersion := ""
	registeredOnly := false
	if task != nil {
		task.RLock()
		protoVersion = task.ProtoVersion
		registeredOnly = task.RegisteredOnly
		task.RUnlock()
	} else if host != nil {
		protoVersion = hostProxyProtocolVersion(host)
		host.RLock()
		registeredOnly = host.RegisteredOnly
		host.RUnlock()
	}
	if isRegisteredOnlyBlocked(client, c.RemoteAddr().String(), registeredOnly) {
		logResult, logError = connectionlog.ResultFailure, "source IP is not registered"
		_ = c.Close()
		return nil
	}

	link := conn.NewLink(tp, addr, clientConfig.Crypt, clientConfig.Compress, c.Conn.RemoteAddr().String(), localProxy, protoVersion)
	link.DatagramFramed = datagramFramed
	if s.bridge == nil {
		_ = c.Close()
		return errors.New("proxy bridge is nil")
	}
	var bridgeTask *file.Tunnel
	if s.task != nil {
		s.task.RLock()
		bridgeTask = &file.Tunnel{Id: s.task.Id, Mode: s.task.Mode, RegisteredOnly: s.task.RegisteredOnly}
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
		userRate := file.UserRateLimiterForClient(client)
		bytesIn, bytesOut = conn.CopyWaitGroupWithUserLimits(target, c.Conn, link.Crypt, link.Compress, clientRate, flow, true, rb, task, host, userRate)
	}
	return nil
}

func proxyConnectionLogEvent(client *file.Client, task *file.Tunnel, host *file.Host, protocol, target, sourceAddr, serverAddr string, startedAt time.Time) connectionlog.Event {
	sourceAddr = strings.TrimSpace(sourceAddr)
	event := connectionlog.Event{
		Time:          startedAt.UTC(),
		StartedAt:     startedAt.UTC(),
		Kind:          connectionlog.KindTunnel,
		Action:        connectionlog.ActionProxyConnection,
		Result:        connectionlog.ResultSuccess,
		ProxyProtocol: strings.TrimSpace(protocol),
		SourceIP:      common.GetIpByAddr(sourceAddr),
		RemoteAddr:    sourceAddr,
		ServerAddr:    strings.TrimSpace(serverAddr),
		Target:        strings.TrimSpace(target),
	}
	if host != nil {
		host.RLock()
		event.Kind = connectionlog.KindHost
		event.ResourceType = "host"
		event.ResourceID = host.Id
		event.ResourceRemark = strings.TrimSpace(host.Host)
		host.RUnlock()
	} else if task != nil {
		task.RLock()
		event.ResourceType = "tunnel"
		event.ResourceID = task.Id
		event.ResourceRemark = strings.TrimSpace(task.Remark)
		if event.ProxyProtocol == "" {
			event.ProxyProtocol = strings.TrimSpace(task.Mode)
		}
		task.RUnlock()
	}
	if client != nil {
		client.RLock()
		event.UserID = client.UserId
		event.UserName = client.UserName
		event.ClientID = client.Id
		event.ClientRemark = strings.TrimSpace(client.Remark)
		event.ClientVersion = strings.TrimSpace(client.Version)
		event.ClientPublicIP = common.GetIpByAddr(client.Addr)
		event.ClientLocalAddr = strings.TrimSpace(client.LocalAddr)
		if event.ClientLocalAddr != "" {
			event.ClientLocalAddrSource = connectionlog.ClientLocalAddrSourceClientRecord
		}
		event.ClientStatus = connectionStatus(client.Status)
		event.ClientExpireTime = strings.TrimSpace(client.ExpireTime)
		event.ClientLastOnlineTime = strings.TrimSpace(client.LastOnlineTime)
		client.RUnlock()
		if effectiveExpire, err := file.GetDb().EffectiveClientExpireTime(client); err == nil {
			event.ClientEffectiveExpireTime = effectiveExpire
		}
		if event.UserID > 0 {
			if user, err := file.GetDb().GetUser(event.UserID); err == nil && user != nil {
				user.RLock()
				if event.UserName == "" {
					event.UserName = user.UserName
				}
				event.UserStatus = connectionStatus(user.Status)
				event.UserExpireTime = strings.TrimSpace(user.ExpireTime)
				user.RUnlock()
			}
		}
	}
	return event
}

func connectionStatus(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func proxyAddrString(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	return strings.TrimSpace(addr.String())
}

// localProxyAllowedForClient applies both the historical global switch and
// the tenant safety gate. Internal synthetic clients (negative IDs) are used
// by server-owned management services and retain access even when ordinary
// user local proxy is disabled.
func localProxyAllowedForClient(client *file.Client, task *file.Tunnel) bool {
	if task != nil {
		task.RLock()
		secret := task.Mode == "secret"
		task.RUnlock()
		if secret && beego.AppConfig.DefaultBool("allow_secret_local", false) {
			return beego.AppConfig.DefaultBool("allow_local_proxy", false)
		}
	}
	return client.LocalProxyAllowed(
		beego.AppConfig.DefaultBool("allow_local_proxy", false),
		beego.AppConfig.DefaultBool("allow_user_local", false),
	)
}

// 判断访问地址是否在全局黑名单内
func IsGlobalBlackIp(ipPort string) bool {
	// Keep the historical function name for callers, but evaluate the complete
	// global policy (blacklist, whitelist, CIDR and priority) here.
	if file.GetDb().GlobalIPDenied(ipPort) {
		logs.Error("IP地址[" + common.GetIpByAddr(ipPort) + "]未通过全局访问控制")
		return true
	}
	return false
}
