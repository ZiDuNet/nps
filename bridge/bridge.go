package bridge

import (
	"context"
	"crypto/tls"
	"ehang.io/nps/lib/nps_mux"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/ipreg"
	"ehang.io/nps/lib/version"
	"ehang.io/nps/server/connection"
	"ehang.io/nps/server/tool"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"
	"github.com/pires/go-proxyproto"
	"github.com/quic-go/quic-go"
)

var ServerTlsEnable bool = false

type Client struct {
	mu        sync.Mutex
	tunnel    *nps_mux.Mux
	signal    *conn.Conn
	file      *nps_mux.Mux
	Version   string
	retryTime atomic.Int32 // it will be add 1 when ping not ok until to 3 will close the client
}

// clientSessionSnapshot identifies the exact resources observed by the ping
// loop. A client entry can be updated in place when a connection reconnects,
// so checking only the client id is not sufficient before deleting it.
type clientSessionSnapshot struct {
	client *Client
	signal *conn.Conn
	tunnel *nps_mux.Mux
	file   *nps_mux.Mux
}

func NewClient(t, f *nps_mux.Mux, s *conn.Conn, vs string) *Client {
	return &Client{
		signal:  s,
		tunnel:  t,
		file:    f,
		Version: vs,
	}
}

func bridgeClient(value interface{}) (*Client, bool) {
	client, ok := value.(*Client)
	return client, ok && client != nil
}

func sanitizePublicClient(client *file.Client) error {
	if client == nil {
		return errors.New("客户端记录无效")
	}
	// Public onboarding may provide credentials and display fields, but the
	// server owns identity and ownership fields.
	client.Lock()
	client.Id = 0
	client.UserId = 0
	client.NoStore = true
	client.NoDisplay = false
	client.Status = true
	client.Unlock()
	return nil
}

// VersionSnapshot returns the negotiated client version without racing the
// handshake path that updates it during reconnects.
func (c *Client) VersionSnapshot() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	version := c.Version
	c.mu.Unlock()
	return version
}

// RequestClientUpdate asks a connected NPC to execute its built-in updater.
// The client version gate is enforced by the Web controller because the
// bridge only owns the live signal connection.
func (s *Bridge) RequestClientUpdate(id int) error {
	if s == nil {
		return errors.New("服务端 Bridge 尚未初始化")
	}
	value, ok := s.Client.Load(id)
	if !ok {
		return fmt.Errorf("客户端 %d 当前未连接", id)
	}
	client, ok := bridgeClient(value)
	if !ok {
		return fmt.Errorf("客户端 %d 控制连接无效", id)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.signal == nil || client.signal.Conn == nil {
		return fmt.Errorf("客户端 %d 当前未连接", id)
	}
	if _, err := client.signal.Write([]byte(common.CLIENT_UPDATE)); err != nil {
		return fmt.Errorf("发送升级指令失败：%w", err)
	}
	return nil
}

func supportsHealthConfig(versionText string) bool {
	comparison, err := version.Compare(versionText, version.HealthConfigMinVersion)
	return err == nil && comparison >= 0
}

// healthProfilesForClient returns the active, persisted health-check profiles
// that belong to one client. Runtime failure counters are deliberately
// omitted: the NPC owns those counters while the server owns the target pool.
// Duplicate profiles are collapsed so a target shared by two resources is
// probed only once by the NPC.
func healthProfilesForClientFromDB(db *file.DbUtils, clientID int) []file.Health {
	if db == nil || db.JsonDb == nil || clientID <= 0 {
		return nil
	}
	profiles := make([]file.Health, 0)
	seen := make(map[string]struct{})
	appendProfile := func(health *file.Health) {
		if !file.HealthConfigEnabled(health) {
			return
		}
		snapshot := file.HealthConfigSnapshot(health)
		// Resources may have been created by older clients or imported from
		// hand-edited JSON. Normalize the copy before deduplicating and sending
		// it so equivalent values such as "HTTP" and "http" share one profile.
		if err := file.NormalizeHealth(&snapshot); err != nil || !file.HealthConfigEnabled(&snapshot) {
			return
		}
		key := fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%d\x00%s",
			snapshot.HealthCheckType,
			snapshot.HealthCheckTarget,
			snapshot.HealthCheckTimeout,
			snapshot.HealthMaxFail,
			snapshot.HealthCheckInterval,
			snapshot.HttpHealthUrl,
		)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		profiles = append(profiles, snapshot)
	}

	db.JsonDb.Tasks.Range(func(_, value interface{}) bool {
		tunnel, ok := value.(*file.Tunnel)
		if !ok || tunnel == nil {
			return true
		}
		tunnel.RLock()
		client := tunnel.Client
		mode, active := tunnel.Mode, tunnel.Status
		tunnel.RUnlock()
		if !active || mode != "tcp" || client == nil {
			return true
		}
		client.RLock()
		owned := client.Id == clientID
		client.RUnlock()
		if owned {
			appendProfile(&tunnel.Health)
		}
		return true
	})
	db.JsonDb.Hosts.Range(func(_, value interface{}) bool {
		host, ok := value.(*file.Host)
		if !ok || host == nil {
			return true
		}
		host.RLock()
		client := host.Client
		active := !host.IsClose
		host.RUnlock()
		if !active || client == nil {
			return true
		}
		client.RLock()
		owned := client.Id == clientID
		client.RUnlock()
		if owned {
			appendProfile(&host.Health)
		}
		return true
	})
	return profiles
}

func healthProfilesForClient(clientID int) []file.Health {
	return healthProfilesForClientFromDB(file.GetDb(), clientID)
}

// RequestClientHealthConfig sends the current effective health configuration
// to a connected, compatible NPC. Offline clients receive the same payload on
// their next WORK_MAIN connection, so callers may safely ignore an offline
// error after persisting a resource change.
func (s *Bridge) RequestClientHealthConfig(id int) error {
	return s.requestClientHealthConfig(id, file.GetDb())
}

func (s *Bridge) requestClientHealthConfig(id int, db *file.DbUtils) error {
	if s == nil {
		return errors.New("服务端 Bridge 尚未初始化")
	}
	value, ok := s.Client.Load(id)
	if !ok {
		return fmt.Errorf("客户端 %d 当前未连接", id)
	}
	client, ok := bridgeClient(value)
	if !ok {
		return fmt.Errorf("客户端 %d 控制连接无效", id)
	}
	if !supportsHealthConfig(client.VersionSnapshot()) {
		// Older NPCs intentionally keep their local [health_*] configuration;
		// sending the new flag would corrupt their control stream.
		return nil
	}
	profiles := healthProfilesForClientFromDB(db, id)
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.signal == nil || client.signal.Conn == nil {
		return fmt.Errorf("客户端 %d 当前未连接", id)
	}
	if _, err := client.signal.SendInfo(profiles, common.HEALTH_CONFIG); err != nil {
		return fmt.Errorf("发送健康检查配置失败：%w", err)
	}
	return nil
}

type Bridge struct {
	TunnelPort     int //通信隧道端口
	Client         sync.Map
	clientsMu      sync.Mutex
	configMu       sync.Mutex
	Register       sync.Map
	tunnelType     string //bridge type kcp or tcp
	OpenTask       chan *file.Tunnel
	CloseTask      chan *file.Tunnel
	CloseClient    chan int
	SecretChan     chan *conn.Secret
	ipVerify       bool
	runList        *sync.Map //map[int]interface{}
	disconnectTime int
}

func clientTunnelQuotaError(client *file.Client) error {
	if client == nil {
		return errors.New("客户端记录无效")
	}
	client.RLock()
	maxTunnelNum, userID := client.MaxTunnelNum, client.UserId
	client.RUnlock()
	if maxTunnelNum > 0 && client.GetTunnelNum() >= maxTunnelNum {
		return errors.New("客户端隧道数量已达到限制")
	}
	if userID > 0 && file.GetDb().IsUserTunnelLimitReached(userID) {
		return errors.New("用户隧道数量已达到限制")
	}
	return nil
}

func NewTunnel(tunnelPort int, tunnelType string, ipVerify bool, runList *sync.Map, disconnectTime int) *Bridge {
	return &Bridge{
		TunnelPort:     tunnelPort,
		tunnelType:     tunnelType,
		OpenTask:       make(chan *file.Tunnel, 128),
		CloseTask:      make(chan *file.Tunnel, 128),
		CloseClient:    make(chan int, 128),
		SecretChan:     make(chan *conn.Secret, 128),
		ipVerify:       ipVerify,
		runList:        runList,
		disconnectTime: disconnectTime,
	}
}

func (s *Bridge) StartTunnel() error {
	if s.tunnelType == "kcp" {
		logs.Info("server start, the bridge type is %s, the bridge port is %d", s.tunnelType, s.TunnelPort)
		kcpListener, err := conn.NewKcpListener(net.JoinHostPort(strings.TrimSpace(beego.AppConfig.String("bridge_ip")), beego.AppConfig.String("bridge_port")))
		if err != nil {
			return err
		}
		if err := s.startExtraBridgeTransports(); err != nil {
			_ = kcpListener.Close()
			return err
		}
		go func() {
			defer kcpListener.Close()
			for {
				c, acceptErr := kcpListener.AcceptKCP()
				if acceptErr != nil {
					if c != nil {
						_ = c.Close()
					}
					logs.Warn(acceptErr)
					return
				}
				if c == nil {
					logs.Warn("kcp listener returned a nil connection")
					return
				}
				conn.SetUdpSession(c)
				go s.cliProcess(conn.NewConn(c), "kcp")
			}
		}()
		go s.ping()
		return nil
	}

	listener, err := connection.GetBridgeListener(s.tunnelType)
	if err != nil {
		return err
	}

	// Bind the optional TLS listener before returning so startup failures are
	// reported to the caller instead of terminating the whole process from a
	// background goroutine.
	var tlsListener net.Listener
	if ServerTlsEnable {
		tlsBridgePort := beego.AppConfig.DefaultInt("tls_bridge_port", 8025)
		logs.Info("tls server start, the bridge type is %s, the tls bridge port is %d", "tcp", tlsBridgePort)
		var tlsErr error
		tlsListener, tlsErr = net.Listen("tcp", net.JoinHostPort(strings.TrimSpace(beego.AppConfig.String("bridge_ip")), strconv.Itoa(tlsBridgePort)))
		if tlsErr != nil {
			_ = listener.Close()
			return tlsErr
		}
	}
	if err := s.startExtraBridgeTransports(); err != nil {
		_ = listener.Close()
		if tlsListener != nil {
			_ = tlsListener.Close()
		}
		return err
	}
	if tlsListener != nil {
		go conn.Accept(tlsListener, func(c net.Conn) {
			s.cliProcess(conn.NewConn(tls.Server(c, &tls.Config{Certificates: []tls.Certificate{crypt.GetCert()}, MinVersion: tls.VersionTLS12})), "tcp")
		})
	}
	go s.ping()
	go conn.Accept(listener, func(c net.Conn) {
		s.cliProcess(conn.NewConn(c), "tcp")
	})
	return nil
}

// startExtraBridgeTransports binds all configured optional endpoints before
// serving any of them. A bad port or certificate therefore fails startup
// instead of leaving a partially enabled Bridge behind.
func (s *Bridge) startExtraBridgeTransports() error {
	type kcpEndpoint struct {
		accept func() (net.Conn, error)
		close  func() error
		port   int
	}
	var kcpEndpointValue *kcpEndpoint
	kcpPort := beego.AppConfig.DefaultInt("bridge_kcp_port", 0)
	if kcpPort > 0 && !(s.tunnelType == "kcp" && kcpPort == s.TunnelPort) {
		listener, err := conn.NewKcpListener(net.JoinHostPort(strings.TrimSpace(beego.AppConfig.String("bridge_ip")), strconv.Itoa(kcpPort)))
		if err != nil {
			return fmt.Errorf("start KCP Bridge on port %d: %w", kcpPort, err)
		}
		kcpEndpointValue = &kcpEndpoint{
			accept: func() (net.Conn, error) {
				session, err := listener.AcceptKCP()
				if err != nil {
					return nil, err
				}
				conn.SetUdpSession(session)
				return session, nil
			},
			close: listener.Close,
			port:  kcpPort,
		}
	}
	websocketListeners := make([]preparedWebSocketBridge, 0, 2)
	for _, endpoint := range []struct {
		key    string
		secure bool
	}{
		{key: "bridge_ws_port"},
		{key: "bridge_wss_port", secure: true},
	} {
		port := beego.AppConfig.DefaultInt(endpoint.key, 0)
		if port <= 0 {
			continue
		}
		prepared, err := s.prepareWebSocketBridge(port, endpoint.secure)
		if err != nil {
			if kcpEndpointValue != nil {
				_ = kcpEndpointValue.close()
			}
			for _, previous := range websocketListeners {
				_ = previous.listener.Close()
			}
			return err
		}
		websocketListeners = append(websocketListeners, prepared)
	}
	var quicListener *quic.Listener
	quicPort := beego.AppConfig.DefaultInt("bridge_quic_port", 0)
	if quicPort > 0 {
		quicIP := strings.TrimSpace(beego.AppConfig.String("bridge_quic_ip"))
		if quicIP == "" {
			quicIP = strings.TrimSpace(beego.AppConfig.String("bridge_ip"))
		}
		if quicIP == "" {
			quicIP = "0.0.0.0"
		}
		alpn := strings.TrimSpace(beego.AppConfig.String("quic_alpn"))
		if alpn == "" {
			alpn = "nps"
		}
		cert := crypt.GetCert()
		quicConfig := &quic.Config{
			KeepAlivePeriod:    time.Duration(beego.AppConfig.DefaultInt("quic_keep_alive_period", 10)) * time.Second,
			MaxIdleTimeout:     time.Duration(beego.AppConfig.DefaultInt("quic_max_idle_timeout", 30)) * time.Second,
			MaxIncomingStreams: int64(beego.AppConfig.DefaultInt("quic_max_incoming_streams", 100000)),
		}
		if quicConfig.KeepAlivePeriod < 0 {
			quicConfig.KeepAlivePeriod = 0
		}
		if quicConfig.MaxIdleTimeout <= 0 {
			quicConfig.MaxIdleTimeout = 30 * time.Second
		}
		if quicConfig.MaxIncomingStreams <= 0 {
			quicConfig.MaxIncomingStreams = 100000
		}
		var quicErr error
		quicListener, quicErr = quic.ListenAddr(net.JoinHostPort(quicIP, strconv.Itoa(quicPort)), &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
			NextProtos:   []string{alpn},
		}, quicConfig)
		if quicErr != nil {
			if kcpEndpointValue != nil {
				_ = kcpEndpointValue.close()
			}
			for _, previous := range websocketListeners {
				_ = previous.listener.Close()
			}
			return fmt.Errorf("start QUIC Bridge on port %d: %w", quicPort, quicErr)
		}
	}
	if kcpEndpointValue != nil {
		logs.Info("server start, the bridge type is kcp, the bridge port is %d", kcpEndpointValue.port)
		go func(endpoint *kcpEndpoint) {
			defer endpoint.close()
			for {
				c, err := endpoint.accept()
				if err != nil {
					if !errors.Is(err, net.ErrClosed) {
						logs.Warn("KCP Bridge accept failed: %v", err)
					}
					return
				}
				if c == nil {
					return
				}
				go s.cliProcess(conn.NewConn(c), "kcp")
			}
		}(kcpEndpointValue)
	}
	for _, prepared := range websocketListeners {
		prepared := prepared
		logs.Info("server start, the bridge type is %s, the bridge port is %d, the bridge path is %s", prepared.transport, prepared.port, prepared.path)
		go func() {
			if err := prepared.server.Serve(prepared.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logs.Error("%s Bridge stopped: %v", prepared.transport, err)
			}
		}()
	}
	if quicListener != nil {
		logs.Info("server start, the bridge type is quic, the bridge port is %d", quicPort)
		go func() {
			err := conn.ServeQuic(context.Background(), quicListener, 10*time.Second, func(c net.Conn) {
				s.cliProcess(conn.NewConn(c), "quic")
			})
			if err != nil && !errors.Is(err, net.ErrClosed) {
				logs.Error("QUIC Bridge stopped: %v", err)
			}
		}()
	}
	return nil
}

// requestClientLocalAddr asks the client for private/LAN IPs on the main
// signal connection. The short deadline keeps older clients compatible.
func (s *Bridge) requestClientLocalAddr(id int, c *conn.Conn) {
	if c == nil || c.Conn == nil {
		return
	}
	if _, err := c.Write([]byte(common.REPORT_LOCAL_IP)); err != nil {
		return
	}
	_ = c.Conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer func() { _ = c.Conn.SetReadDeadline(time.Time{}) }()
	b, err := c.GetShortLenContent()
	if err != nil {
		logs.Trace("[兼容] clientId %d 未上报内网地址（旧客户端或超时）：%v", id, err)
		return
	}
	localAddr := strings.TrimSpace(string(b))
	if localAddr == "" {
		return
	}
	if client, err := file.GetDb().GetClient(id); err == nil {
		client.Lock()
		client.LocalAddr = localAddr
		client.Unlock()
		logs.Info("[客户端] clientId %d 内网地址：%s", id, localAddr)
	}
}

func matchesHealthClient(client *file.Client, id int) bool {
	if client == nil {
		return false
	}
	client.RLock()
	clientID := client.Id
	client.RUnlock()
	return clientID == id
}

func applyHealthResult(target *file.Target, health *file.Health, info string, healthy bool) {
	if target == nil || health == nil || !strings.Contains(target.TargetStr, info) {
		return
	}
	health.HealthLock()
	defer health.HealthUnlock()
	if !healthy {
		if target.TargetArr == nil || (len(target.TargetArr) == 0 && len(health.HealthRemoveArr) == 0) {
			target.TargetArr = common.TrimArr(strings.Split(target.TargetStr, "\n"))
		}
		target.TargetArr = common.RemoveArrVal(target.TargetArr, info)
		if !common.IsArrContains(health.HealthRemoveArr, info) {
			health.HealthRemoveArr = append(health.HealthRemoveArr, info)
		}
		return
	}
	if common.IsArrContains(health.HealthRemoveArr, info) && !common.IsArrContains(target.TargetArr, info) {
		target.TargetArr = append(target.TargetArr, info)
	}
	health.HealthRemoveArr = common.RemoveArrVal(health.HealthRemoveArr, info)
}

func updateTunnelHealth(tunnel *file.Tunnel, clientID int, info string, healthy bool) {
	if tunnel == nil {
		return
	}
	tunnel.RLock()
	client, target, mode := tunnel.Client, tunnel.Target, tunnel.Mode
	tunnel.RUnlock()
	if mode != "tcp" || target == nil || !matchesHealthClient(client, clientID) {
		return
	}
	tunnel.Lock()
	defer tunnel.Unlock()
	if tunnel.Client != client || tunnel.Target != target || tunnel.Mode != "tcp" {
		return
	}
	target.Lock()
	defer target.Unlock()
	applyHealthResult(target, &tunnel.Health, info, healthy)
}

func updateHostHealth(host *file.Host, clientID int, info string, healthy bool) {
	if host == nil {
		return
	}
	host.RLock()
	client, target := host.Client, host.Target
	host.RUnlock()
	if target == nil || !matchesHealthClient(client, clientID) {
		return
	}
	host.Lock()
	defer host.Unlock()
	if host.Client != client || host.Target != target {
		return
	}
	target.Lock()
	defer target.Unlock()
	applyHealthResult(target, &host.Health, info, healthy)
}

// GetHealthFromClient updates the target pool reported by one client. It
// snapshots the object graph before mutation so malformed runtime data and
// simultaneous console edits cannot panic this control path.
func (s *Bridge) GetHealthFromClient(id int, c *conn.Conn) {
	if c == nil || c.Conn == nil {
		return
	}
	for {
		info, healthy, err := c.GetHealthInfo()
		if err != nil {
			break
		}
		file.GetDb().JsonDb.Tasks.Range(func(_, value interface{}) bool {
			tunnel, ok := value.(*file.Tunnel)
			if ok {
				updateTunnelHealth(tunnel, id, info, healthy)
			}
			return true
		})
		file.GetDb().JsonDb.Hosts.Range(func(_, value interface{}) bool {
			host, ok := value.(*file.Host)
			if ok {
				updateHostHealth(host, id, info, healthy)
			}
			return true
		})
	}
	// A reconnect can replace the main connection while this goroutine is
	// blocked in GetHealthInfo. Do not let the old reader tear down the newer
	// session when its socket eventually reports EOF.
	s.delClientIfCurrentSignal(id, c)
}

// 验证失败，返回错误验证flag，并且关闭连接
func (s *Bridge) verifyError(c *conn.Conn) {
	c.Write([]byte(common.VERIFY_EER))
	c.Close()
}

func (s *Bridge) verifySuccess(c *conn.Conn) {
	c.Write([]byte(common.VERIFY_SUCCESS))
}

func (s *Bridge) cliProcess(c *conn.Conn, transport ...string) {
	if c == nil || c.Conn == nil {
		return
	}
	transportType := s.tunnelType
	if len(transport) > 0 && transport[0] != "" {
		transportType = transport[0]
	}
	// Apply the administrator's global source policy before reading the
	// handshake. This protects the bridge/control plane as well as visitor
	// proxy listeners and avoids spending handshake resources on denied peers.
	if file.GetDb().GlobalIPDenied(c.Conn.RemoteAddr().String()) {
		logs.Warn("[访问控制] 拒绝桥接来源 %s", c.Conn.RemoteAddr())
		_ = c.Close()
		return
	}
	// Bound the handshake so half-open/scanning connections do not retain a goroutine.
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	//read test flag
	if _, err := c.GetShortContent(3); err != nil {
		logs.Info("[连接异常] 客户端 %s 握手失败：%s", c.Conn.RemoteAddr(), err.Error())
		c.Close()
		return
	}
	//version check
	if b, err := c.GetShortLenContent(); err != nil || string(b) != version.GetVersion() {
		//logs.Info("The client %s version does not match", c.Conn.RemoteAddr())
		//c.Close()
		//return
	}
	//version get
	var vs []byte
	var err error
	if vs, err = c.GetShortLenContent(); err != nil {
		logs.Info("[连接异常] 读取客户端 %s 版本失败：%s", c.Conn.RemoteAddr(), err.Error())
		c.Close()
		return
	}
	clientVersion := strings.TrimSpace(strings.Trim(string(vs), "\x00"))
	id, err := s.authenticateClient(c, clientVersion, file.GetDb())
	if err != nil {
		logs.Info("[验证失败] 拒绝客户端连接 %s，版本 %q，原因：%v", c.Conn.RemoteAddr(), clientVersion, err)
		_ = c.Close()
		return
	}
	if flag, err := c.ReadFlag(); err == nil {
		_ = c.SetReadDeadline(time.Time{})
		s.typeDeal(flag, c, id, clientVersion, transportType)
	} else {
		logs.Warn("[连接异常] 读取客户端连接类型失败：%v，flag=%q", err, flag)
		c.Close()
	}
	return
}

// authenticateClient chooses the wire-compatible handshake using the NPC's
// product release. NPC 1.1.28 and earlier receive only the original MD5(vkey)
// exchange; new messages are reserved for 1.1.29 and later.
func (s *Bridge) authenticateClient(c *conn.Conn, clientVersion string, db *file.DbUtils) (int, error) {
	switch {
	case version.UsesSecureHandshake(clientVersion):
		return s.secureClientHandshakeWithDB(c, clientVersion, db)
	case version.SupportsLegacyHandshake(clientVersion):
		if c == nil || c.Conn == nil {
			return 0, errors.New("invalid handshake connection")
		}
		if db == nil || db.JsonDb == nil {
			return 0, errors.New("client database is unavailable")
		}
		if _, err := c.Write([]byte(crypt.Md5(version.GetVersion()))); err != nil {
			return 0, fmt.Errorf("write legacy server handshake: %w", err)
		}
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
		proof, err := c.GetShortContent(32)
		if err != nil {
			return 0, fmt.Errorf("read legacy client proof: %w", err)
		}
		id, err := db.GetIdByVerifyKey(string(proof), c.Conn.RemoteAddr().String())
		if err != nil {
			s.verifyError(c)
			return 0, err
		}
		s.verifySuccess(c)
		return id, nil
	default:
		return 0, fmt.Errorf("不支持的 NPC 版本号 %q", clientVersion)
	}
}

// secureClientHandshake performs the 1.1.29+ challenge-response handshake.
// The server sends a fixed marker first, then proves possession of the
// matching client key before accepting a Bridge connection type.
func (s *Bridge) secureClientHandshake(c *conn.Conn, clientVersion string) (int, error) {
	return s.secureClientHandshakeWithDB(c, clientVersion, file.GetDb())
}

func (s *Bridge) secureClientHandshakeWithDB(c *conn.Conn, clientVersion string, db *file.DbUtils) (int, error) {
	if c == nil || c.Conn == nil {
		return 0, errors.New("invalid handshake connection")
	}
	if db == nil || db.JsonDb == nil {
		return 0, errors.New("client database is unavailable")
	}
	if _, err := c.Write(crypt.SecureHandshakeMarker()); err != nil {
		return 0, fmt.Errorf("write secure handshake marker: %w", err)
	}
	keyID, err := c.GetShortLenContent()
	if err != nil {
		return 0, fmt.Errorf("read client key identifier: %w", err)
	}
	clientRecord, verifyKey, lookupErr := db.GetClientBySecureKeyID(keyID)
	challenge, err := crypt.NewSecureHandshakeChallenge()
	if err != nil {
		return 0, fmt.Errorf("create challenge: %w", err)
	}
	if err := c.WriteLenContent(challenge); err != nil {
		return 0, fmt.Errorf("send challenge: %w", err)
	}
	payload, err := c.GetShortLenContent()
	if err != nil {
		return 0, fmt.Errorf("read client proof: %w", err)
	}
	if lookupErr != nil || clientRecord == nil {
		return 0, errors.New("client authentication failed")
	}
	window := int64(beego.AppConfig.DefaultInt("auth_time_window", 300))
	if err := crypt.VerifySecureHandshakeClientPayload(verifyKey, version.GetVersion(), clientVersion, payload, challenge, time.Now().Unix(), window); err != nil {
		return 0, err
	}
	if !db.IsClientActive(clientRecord) {
		return 0, errors.New("客户端已停用或已过期")
	}
	clientRecord.Lock()
	if clientRecord.VerifyKey != verifyKey || !clientRecord.Status {
		clientRecord.Unlock()
		return 0, errors.New("client credentials changed during handshake")
	}
	clientID := clientRecord.Id
	clientRecord.Addr = common.GetIpByAddr(c.Conn.RemoteAddr().String())
	clientRecord.Unlock()
	serverProof, err := crypt.SecureHandshakeServerProof(verifyKey, payload, challenge)
	if err != nil {
		return 0, err
	}
	s.verifySuccess(c)
	if _, err := c.Write(serverProof); err != nil {
		return 0, fmt.Errorf("write server proof: %w", err)
	}
	return clientID, nil
}

func (s *Bridge) DelClient(id int) {
	s.delClient(id, nil)
}

func (s *Bridge) delClientIfCurrentSignal(id int, signal *conn.Conn) {
	s.delClient(id, signal)
}

// delClientIfSnapshot removes a client only when the map entry and all
// session resources still match the snapshot taken by the caller. This keeps
// a stale ping/health goroutine from deleting a freshly reconnected session.
func (s *Bridge) delClientIfSnapshot(id int, snapshot clientSessionSnapshot) {
	s.clientsMu.Lock()
	v, ok := s.Client.Load(id)
	if !ok {
		s.clientsMu.Unlock()
		return
	}
	cl, ok := v.(*Client)
	if !ok || cl != snapshot.client {
		s.clientsMu.Unlock()
		return
	}
	cl.mu.Lock()
	if cl.signal != snapshot.signal || cl.tunnel != snapshot.tunnel || cl.file != snapshot.file {
		cl.mu.Unlock()
		s.clientsMu.Unlock()
		return
	}
	s.Client.Delete(id)
	signal, tunnel, fileMux := cl.signal, cl.tunnel, cl.file
	wasOnline := signal != nil
	cl.signal, cl.tunnel, cl.file = nil, nil, nil
	cl.mu.Unlock()
	if wasOnline {
		s.recordClientConnection(id, false)
	}
	s.clientsMu.Unlock()

	if signal != nil {
		_ = signal.Close()
	}
	if tunnel != nil {
		_ = tunnel.Close()
	}
	if fileMux != nil {
		_ = fileMux.Close()
	}
	if file.GetDb().IsPubClient(id) {
		return
	}
	if c, err := file.GetDb().GetClient(id); err == nil {
		select {
		case s.CloseClient <- c.Id:
		default:
		}
	}
}

// delClient serializes a main-connection replacement with cleanup. When
// expectedSignal is set, the delete applies only if that signal is still the
// active one for the client.
func (s *Bridge) delClient(id int, expectedSignal *conn.Conn) {
	s.clientsMu.Lock()
	v, ok := s.Client.Load(id)
	if !ok {
		s.clientsMu.Unlock()
		return
	}
	cl, valid := bridgeClient(v)
	if !valid {
		// A malformed runtime entry must not crash the cleanup goroutine.
		s.Client.Delete(id)
		s.clientsMu.Unlock()
		return
	}
	cl.mu.Lock()
	if expectedSignal != nil && cl.signal != expectedSignal {
		cl.mu.Unlock()
		s.clientsMu.Unlock()
		return
	}
	s.Client.Delete(id)
	signal, tunnel, fileMux := cl.signal, cl.tunnel, cl.file
	wasOnline := signal != nil
	cl.signal, cl.tunnel, cl.file = nil, nil, nil
	cl.mu.Unlock()
	if wasOnline {
		s.recordClientConnection(id, false)
	}
	s.clientsMu.Unlock()

	if signal != nil {
		_ = signal.Close()
	}
	if tunnel != nil {
		_ = tunnel.Close()
	}
	if fileMux != nil {
		_ = fileMux.Close()
	}
	if file.GetDb().IsPubClient(id) {
		return
	}
	if c, err := file.GetDb().GetClient(id); err == nil {
		select {
		case s.CloseClient <- c.Id:
		default:
		}
	}
}

// recordClientConnection runs under clientsMu so a rapid reconnect cannot
// reorder an older offline event after the new online transition.
func (s *Bridge) recordClientConnection(id int, online bool) {
	if client, err := file.GetDb().GetClient(id); err == nil && client != nil {
		client.RecordConnection(online, time.Now())
	}
}

// use different
func (s *Bridge) typeDeal(typeVal string, c *conn.Conn, id int, vs string, transport ...string) {
	transportType := s.tunnelType
	if len(transport) > 0 && transport[0] != "" {
		transportType = transport[0]
	}
	isPub := file.GetDb().IsPubClient(id)
	switch typeVal {
	case common.WORK_MAIN:
		if isPub {
			c.Close()
			return
		}
		tcpConn, ok := c.Conn.(*net.TCPConn)
		if ok {
			// add tcp keep alive option for signal connection
			_ = tcpConn.SetKeepAlive(true)
			_ = tcpConn.SetKeepAlivePeriod(5 * time.Second)
		}
		// The same vKey may reconnect while the old health reader is still
		// blocked. Serialize the replacement with client cleanup, then close the
		// old socket so that reader can exit without retaining resources.
		var oldSignal *conn.Conn
		s.clientsMu.Lock()
		wasOnline := false
		if v, ok := s.Client.Load(id); ok {
			if cl, valid := bridgeClient(v); valid {
				cl.mu.Lock()
				oldSignal = cl.signal
				wasOnline = oldSignal != nil
				cl.signal = c
				cl.Version = vs
				cl.mu.Unlock()
			} else {
				s.Client.Delete(id)
				s.Client.Store(id, NewClient(nil, nil, c, vs))
			}
		} else {
			s.Client.Store(id, NewClient(nil, nil, c, vs))
		}
		if !wasOnline {
			s.recordClientConnection(id, true)
		}
		s.clientsMu.Unlock()
		if oldSignal != nil && oldSignal != c {
			_ = oldSignal.Close()
		}
		s.requestClientLocalAddr(id, c)
		if err := s.RequestClientHealthConfig(id); err != nil {
			// An offline/reconnecting data channel must not make the control
			// connection fail. The next reconnect repeats this synchronization.
			logs.Trace("[配置] clientId %d 健康检查配置未下发：%v", id, err)
		}
		go s.GetHealthFromClient(id, c)
		logs.Info("[连接] clientId %d 已连接，来源：%s", id, c.Conn.RemoteAddr())
	case common.WORK_CHAN:
		muxConn := nps_mux.NewMux(c.Conn, transportType, s.disconnectTime)
		var oldTunnel *nps_mux.Mux
		s.clientsMu.Lock()
		if v, ok := s.Client.Load(id); ok {
			if cl, valid := bridgeClient(v); valid {
				cl.mu.Lock()
				oldTunnel = cl.tunnel
				cl.tunnel = muxConn
				cl.mu.Unlock()
			} else {
				s.Client.Delete(id)
				s.Client.Store(id, NewClient(muxConn, nil, nil, vs))
			}
		} else {
			s.Client.Store(id, NewClient(muxConn, nil, nil, vs))
		}
		s.clientsMu.Unlock()
		if oldTunnel != nil {
			_ = oldTunnel.Close()
		}
	case common.WORK_CONFIG:
		client, err := file.GetDb().GetClient(id)
		if err != nil || client == nil {
			c.Close()
			return
		}
		client.RLock()
		configConnAllow := client.ConfigConnAllow
		client.RUnlock()
		if !isPub && !configConnAllow {
			c.Close()
			return
		}
		binary.Write(c, binary.LittleEndian, isPub)
		go s.getConfig(c, isPub, client)
	case common.WORK_REGISTER:
		go s.register(c)
	case common.WORK_SECRET:
		if b, err := c.GetShortContent(32); err == nil {
			s.SecretChan <- conn.NewSecret(string(b), c)
		} else {
			logs.Error("secret error, failed to match the key successfully")
			c.Close()
		}
	case common.WORK_FILE:
		muxConn := nps_mux.NewMux(c.Conn, transportType, s.disconnectTime)
		var oldFile *nps_mux.Mux
		s.clientsMu.Lock()
		if v, ok := s.Client.Load(id); ok {
			if cl, valid := bridgeClient(v); valid {
				cl.mu.Lock()
				oldFile = cl.file
				cl.file = muxConn
				cl.mu.Unlock()
			} else {
				s.Client.Delete(id)
				s.Client.Store(id, NewClient(nil, muxConn, nil, vs))
			}
		} else {
			s.Client.Store(id, NewClient(nil, muxConn, nil, vs))
		}
		s.clientsMu.Unlock()
		if oldFile != nil {
			_ = oldFile.Close()
		}
	case common.WORK_P2P:
		//read md5 secret
		if b, err := c.GetShortContent(32); err != nil {
			logs.Error("p2p error,", err.Error())
			c.Close()
		} else if t := file.GetDb().GetTaskByMd5Password(string(b)); t == nil {
			logs.Error("p2p error, failed to match the key successfully")
			c.Close()
		} else {
			t.RLock()
			taskStatus, taskMode, taskClient := t.Status, t.Mode, t.Client
			t.RUnlock()
			if !taskStatus || taskMode != "p2p" || taskClient == nil {
				logs.Error("p2p error, task is inactive or malformed")
				c.Close()
				return
			}
			taskClient.RLock()
			clientID := taskClient.Id
			taskClient.RUnlock()
			if v, ok := s.Client.Load(clientID); !ok {
				c.Close()
				return
			} else {
				cl, valid := bridgeClient(v)
				if !valid {
					_ = c.Close()
					return
				}
				cl.mu.Lock()
				sig := cl.signal
				cl.mu.Unlock()
				if sig == nil {
					logs.Error("p2p error, client signal is nil")
					c.Close()
					return
				}
				if _, err := sig.Write([]byte(common.NEW_UDP_CONN)); err != nil {
					_ = c.Close()
					return
				}
				svrAddr := beego.AppConfig.String("p2p_ip") + ":" + beego.AppConfig.String("p2p_port")
				if err := sig.WriteLenContent([]byte(svrAddr)); err != nil {
					_ = c.Close()
					return
				}
				if err := sig.WriteLenContent(b); err != nil {
					_ = c.Close()
					return
				}
				if err := c.WriteLenContent([]byte(svrAddr)); err != nil {
					_ = c.Close()
				}
			}
		}
		return
	}
	c.SetAlive(transportType)
	return
}

// register ip
func (s *Bridge) register(c *conn.Conn) {
	var hour int32
	if err := binary.Read(c, binary.LittleEndian, &hour); err == nil {
		s.Register.Store(common.GetIpByAddr(c.Conn.RemoteAddr().String()), time.Now().Add(time.Hour*time.Duration(hour)))
	}
}

func (s *Bridge) SendLinkInfo(clientId int, link *conn.Link, t *file.Tunnel) (target net.Conn, err error) {
	if s == nil || link == nil {
		return nil, errors.New("invalid link")
	}
	//if the proxy type is local
	if link.LocalProxy {
		if !beego.AppConfig.DefaultBool("allow_local_proxy", false) {
			return nil, errors.New("local proxy is disabled")
		}
		target, err = net.Dial("tcp", link.Host)
		if err == nil && (link.ProtoVersion == "V1" || link.ProtoVersion == "V2") {
			if headerErr := writeLocalProxyProtocolHeader(target, link.RemoteAddr, link.ProtoVersion); headerErr != nil {
				_ = target.Close()
				return nil, headerErr
			}
		}
		return
	}
	if v, ok := s.Client.Load(clientId); ok {
		clientRecord, _ := file.GetDb().GetClient(clientId)
		if registeredIPRequired(clientRecord, t) && !ipreg.RegisteredString(clientId, link.RemoteAddr) {
			return nil, fmt.Errorf("访问 IP %s 尚未完成登记", common.GetIpByAddr(link.RemoteAddr))
		}
		//If ip is restricted to do ip verification
		if s.ipVerify {
			ip := common.GetIpByAddr(link.RemoteAddr)
			registered, ok := s.Register.Load(ip)
			if !ok {
				return nil, errors.New(fmt.Sprintf("The ip %s is not in the validation list", ip))
			}
			expiresAt, valid := registered.(time.Time)
			if !valid || !expiresAt.After(time.Now()) {
				return nil, errors.New(fmt.Sprintf("The validity of the ip %s has expired", ip))
			}
		}
		cl, valid := bridgeClient(v)
		if !valid {
			return nil, errors.New(fmt.Sprintf("the client %d record is invalid", clientId))
		}
		cl.mu.Lock()
		var tunnel *nps_mux.Mux
		if t != nil && t.Mode == "file" {
			tunnel = cl.file
		} else {
			tunnel = cl.tunnel
		}
		cl.mu.Unlock()
		if tunnel == nil {
			err = errors.New("the client connect error")
			return
		}
		if target, err = tunnel.NewConn(); err != nil {
			return
		}
		if t != nil && t.Mode == "file" {
			//TODO if t.mode is file ,not use crypt or compress
			link.Crypt = false
			link.Compress = false
			return
		}
		if _, err = conn.NewConn(target).SendInfo(link, ""); err != nil {
			logs.Info("new connect error ,the target %s refuse to connect", link.Host)
			// NewConn has already reserved a mux stream. If the link metadata
			// cannot be sent, close the stream so it is removed from the mux map
			// and the remote side is notified instead of leaking indefinitely.
			_ = target.Close()
			return
		}
	} else {
		err = errors.New(fmt.Sprintf("the client %d is not connect", clientId))
	}
	return
}

func writeLocalProxyProtocolHeader(target net.Conn, remoteAddr, protoVersion string) error {
	if target == nil {
		return errors.New("PROXY protocol target is nil")
	}
	var version byte
	switch protoVersion {
	case "V1":
		version = 1
	case "V2":
		version = 2
	default:
		return fmt.Errorf("unsupported PROXY protocol version %q", protoVersion)
	}
	sourceAddr, err := net.ResolveTCPAddr("tcp", strings.TrimSpace(remoteAddr))
	if err != nil || sourceAddr == nil || sourceAddr.IP == nil {
		return fmt.Errorf("invalid source address for PROXY protocol: %q", remoteAddr)
	}
	destinationAddr, ok := target.RemoteAddr().(*net.TCPAddr)
	if !ok || destinationAddr == nil || destinationAddr.IP == nil {
		return fmt.Errorf("invalid target address for PROXY protocol: %v", target.RemoteAddr())
	}
	sourceIsIPv4 := sourceAddr.IP.To4() != nil
	destinationIsIPv4 := destinationAddr.IP.To4() != nil
	if sourceIsIPv4 != destinationIsIPv4 {
		return fmt.Errorf("PROXY protocol cannot represent mixed address families: %s -> %s", sourceAddr, destinationAddr)
	}
	transport := proxyproto.TCPv4
	if !sourceIsIPv4 {
		transport = proxyproto.TCPv6
	}
	header := &proxyproto.Header{
		Command:           proxyproto.PROXY,
		SourceAddr:        sourceAddr,
		DestinationAddr:   destinationAddr,
		Version:           version,
		TransportProtocol: transport,
	}
	_, err = header.WriteTo(target)
	if err != nil {
		return fmt.Errorf("write PROXY protocol header: %w", err)
	}
	return nil
}

func (s *Bridge) ping() {
	ticker := time.NewTicker(time.Second * 5)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			arr := make([]struct {
				id       int
				snapshot clientSessionSnapshot
			}, 0)
			s.Client.Range(func(key, value interface{}) bool {
				id, ok := key.(int)
				if !ok {
					return true
				}
				v, ok := value.(*Client)
				if !ok || v == nil {
					return true
				}
				v.mu.Lock()
				snapshot := clientSessionSnapshot{
					client: v,
					signal: v.signal,
					tunnel: v.tunnel,
					file:   v.file,
				}
				isClose := snapshot.tunnel != nil && snapshot.tunnel.IsClose()
				healthy := snapshot.tunnel != nil && snapshot.signal != nil && !isClose
				v.mu.Unlock()
				if healthy {
					// A successful health cycle clears prior transient misses;
					// otherwise three old misses can evict a live reconnect.
					v.retryTime.Store(0)
					return true
				}
				if snapshot.tunnel == nil || snapshot.signal == nil {
					v.retryTime.Add(1)
					if v.retryTime.Load() >= 3 {
						arr = append(arr, struct {
							id       int
							snapshot clientSessionSnapshot
						}{id: id, snapshot: snapshot})
					}
					return true
				}
				if isClose {
					arr = append(arr, struct {
						id       int
						snapshot clientSessionSnapshot
					}{id: id, snapshot: snapshot})
				}
				return true
			})
			for _, v := range arr {
				logs.Info("[连接] clientId %d 会话已关闭", v.id)
				s.delClientIfSnapshot(v.id, v.snapshot)
			}
			// 清理过期的 Register 条目
			now := time.Now()
			s.Register.Range(func(key, value interface{}) bool {
				expiresAt, valid := value.(time.Time)
				if !valid || expiresAt.Before(now) {
					s.Register.Delete(key)
				}
				return true
			})
		}
	}
}

func registeredIPRequired(client *file.Client, task *file.Tunnel) bool {
	required := task != nil && task.RegisteredOnly
	if client == nil {
		return required
	}
	client.RLock()
	required = required || client.RegisteredOnly
	client.RUnlock()
	return required
}

// get config and add task from client config
func (s *Bridge) getConfig(c *conn.Conn, isPub bool, client *file.Client) {
	var fail bool
loop:
	for {
		flag, err := c.ReadFlag()
		if err != nil {
			break
		}
		switch flag {
		case common.WORK_STATUS:
			if b, err := c.GetShortContent(32); err != nil {
				break loop
			} else {
				var str string
				id, err := file.GetDb().GetClientIdByVkey(string(b))
				if err != nil {
					break loop
				}
				file.GetDb().JsonDb.Hosts.Range(func(key, value interface{}) bool {
					v, ok := value.(*file.Host)
					if !ok || v == nil {
						return true
					}
					v.RLock()
					client, remark := v.Client, v.Remark
					v.RUnlock()
					if client != nil {
						client.RLock()
						clientID := client.Id
						client.RUnlock()
						if clientID == id {
							str += remark + common.CONN_DATA_SEQ
						}
					}
					return true
				})
				file.GetDb().JsonDb.Tasks.Range(func(key, value interface{}) bool {
					v, ok := value.(*file.Tunnel)
					if !ok || v == nil {
						return true
					}
					v.RLock()
					taskID, client, remark := v.Id, v.Client, v.Remark
					v.RUnlock()
					if client != nil {
						client.RLock()
						clientID := client.Id
						client.RUnlock()
						if s.runList != nil {
							if _, running := s.runList.Load(taskID); running && clientID == id {
								str += remark + common.CONN_DATA_SEQ
							}
						}
					}
					return true
				})
				binary.Write(c, binary.LittleEndian, int32(len([]byte(str))))
				binary.Write(c, binary.LittleEndian, []byte(str))
			}
		case common.NEW_CONF:
			if !isPub {
				// An established client may exchange its own hosts/tunnels, but
				// it must never submit a replacement Client record. Public-key
				// onboarding is the only flow that creates a client here.
				c.WriteAddFail()
				break loop
			}
			var err error
			var candidate *file.Client
			if candidate, err = c.GetConfigInfo(); err != nil {
				fail = true
				c.WriteAddFail()
				break loop
			} else {
				if err = sanitizePublicClient(candidate); err != nil {
					fail = true
					c.WriteAddFail()
					break loop
				}
				if err = file.GetDb().NewClient(candidate); err != nil {
					fail = true
					c.WriteAddFail()
					break loop
				}
				client = candidate
				c.WriteAddOk()
				c.Write([]byte(client.VerifyKey))
				s.Client.Store(client.Id, NewClient(nil, nil, nil, ""))
			}
		case common.NEW_HOST:
			h, err := c.GetHostInfo()
			if err != nil {
				fail = true
				c.WriteAddFail()
				break loop
			}
			if client == nil {
				fail = true
				c.WriteAddFail()
				break loop
			}
			if h.Target != nil && !beego.AppConfig.DefaultBool("allow_local_proxy", false) {
				h.Target.Lock()
				h.Target.LocalProxy = false
				h.Target.Unlock()
			}
			h.Client = client
			if h.Location == "" {
				h.Location = "/"
			}
			s.configMu.Lock()
			hasHost := client.HasHost(h)
			if !hasHost {
				if err := clientTunnelQuotaError(client); err != nil {
					s.configMu.Unlock()
					fail = true
					c.WriteAddFail()
					break loop
				}
				if file.GetDb().IsHostExist(h) {
					s.configMu.Unlock()
					fail = true
					c.WriteAddFail()
					break loop
				}
				if err := file.GetDb().NewHost(h); err != nil {
					s.configMu.Unlock()
					fail = true
					c.WriteAddFail()
					break loop
				}
			}
			s.configMu.Unlock()
			c.WriteAddOk()
		case common.NEW_TASK:
			if t, err := c.GetTaskInfo(); err != nil {
				fail = true
				c.WriteAddFail()
				break loop
			} else {
				if t.Target != nil && !beego.AppConfig.DefaultBool("allow_local_proxy", false) {
					t.Target.Lock()
					t.Target.LocalProxy = false
					t.Target.Unlock()
				}
				ports := common.GetPorts(t.Ports)
				targets := common.GetPorts(t.Target.TargetStr)
				if len(ports) > 1 && (t.Mode == "tcp" || t.Mode == "udp") && (len(ports) != len(targets)) {
					fail = true
					c.WriteAddFail()
					break loop
				} else if t.Mode == "secret" || t.Mode == "p2p" {
					ports = append(ports, 0)
				}
				if len(ports) == 0 {
					fail = true
					c.WriteAddFail()
					break loop
				}
				for i := 0; i < len(ports); i++ {
					tl := new(file.Tunnel)
					tl.Mode = t.Mode
					tl.Port = ports[i]
					tl.ServerIp = t.ServerIp
					if len(ports) == 1 {
						tl.Target = t.Target
						tl.Remark = t.Remark
					} else {
						tl.Remark = t.Remark + "_" + strconv.Itoa(tl.Port)
						tl.Target = new(file.Target)
						if t.TargetAddr != "" {
							tl.Target.TargetStr = t.TargetAddr + ":" + strconv.Itoa(targets[i])
						} else {
							tl.Target.TargetStr = strconv.Itoa(targets[i])
						}
					}
					tl.Id = int(file.GetDb().JsonDb.GetTaskId())
					tl.Status = true
					tl.Flow = new(file.Flow)
					tl.NoStore = true
					tl.Client = client
					tl.Password = t.Password
					tl.LocalPath = t.LocalPath
					tl.StripPre = t.StripPre
					tl.MultiAccount = t.MultiAccount
					tl.Health = file.HealthConfigSnapshot(&t.Health)
					s.configMu.Lock()
					hasTunnel := client.HasTunnel(tl)
					if !hasTunnel {
						if err := clientTunnelQuotaError(client); err != nil {
							s.configMu.Unlock()
							fail = true
							c.WriteAddFail()
							break loop
						}
						if err := file.GetDb().NewTask(tl); err != nil {
							logs.Notice("Add task error ", err.Error())
							s.configMu.Unlock()
							fail = true
							c.WriteAddFail()
							break loop
						}
					}
					s.configMu.Unlock()
					if !hasTunnel {
						if b := tool.TestServerPort(tl.Port, tl.Mode); !b && t.Mode != "secret" && t.Mode != "p2p" {
							// Remove the record if its listener cannot be reserved.
							file.GetDb().DelTask(tl.Id)
							fail = true
							c.WriteAddFail()
							break loop
						}
						select {
						case s.OpenTask <- tl:
						default:
							// The command queue is bounded; avoid retaining a task that can
							// never be started when the server is under load.
							file.GetDb().DelTask(tl.Id)
							fail = true
							c.WriteAddFail()
							break loop
						}
					}
					c.WriteAddOk()
				}
			}
		}
	}
	if fail && client != nil {
		s.DelClient(client.Id)
	}
	c.Close()
}
