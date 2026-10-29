package bridge

import (
	"context"
	"crypto/tls"
	"ehang.io/nps/lib/nps_mux"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/connectionlog"
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
	mu           sync.Mutex
	sendMu       sync.Mutex // serializes Bridge-initiated frames on signal
	tunnel       *nps_mux.Mux
	signal       *conn.Conn
	file         *nps_mux.Mux
	fileChannels map[string]*nps_mux.Mux
	Version      string
	Transport    string
	retryTime    atomic.Int32 // it will be add 1 when ping not ok until to 3 will close the client
	parent       *Client
	sessions     []*Client
	selectSeq    atomic.Uint64
}

// Node is the small compatibility view used by the modern P2P coordinator.
// This server stores one live control/data pair per client; the view keeps
// the coordinator independent from that storage detail.
type Node struct{ client *Client }

func (n *Node) GetSignal() *conn.Conn {
	if n == nil || n.client == nil {
		return nil
	}
	n.client.mu.Lock()
	signal := n.client.signal
	n.client.mu.Unlock()
	return signal
}

func (n *Node) versions() (int, string) {
	if n == nil || n.client == nil {
		return 0, ""
	}
	return 5, n.client.VersionSnapshot()
}

func (n *Node) sendSignal(flag string, value any, timeout time.Duration) error {
	if n == nil || n.client == nil {
		return errors.New("no signal connection")
	}
	n.client.mu.Lock()
	signal := n.client.signal
	n.client.mu.Unlock()
	if signal == nil || signal.Conn == nil {
		return errors.New("no signal connection")
	}
	n.client.sendMu.Lock()
	defer n.client.sendMu.Unlock()
	_ = signal.Conn.SetWriteDeadline(time.Now().Add(timeout))
	_, err := signal.SendInfo(value, flag)
	_ = signal.Conn.SetWriteDeadline(time.Time{})
	if err != nil {
		_ = signal.Close()
	}
	return err
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

func (c *Client) rootClient() *Client {
	if c == nil {
		return nil
	}
	if c.parent != nil {
		return c.parent
	}
	return c
}

func ensureClientSessions(root *Client) {
	if root == nil {
		return
	}
	if len(root.sessions) == 0 {
		root.sessions = []*Client{root}
	}
}

func sessionHasResource(session *Client, resource string) bool {
	if session == nil {
		return false
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	switch resource {
	case "signal":
		return session.signal != nil && session.signal.Conn != nil
	case "tunnel":
		return session.tunnel != nil && !session.tunnel.IsClose()
	case "file":
		return session.file != nil && !session.file.IsClose()
	case "online":
		return session.signal != nil && session.signal.Conn != nil && session.tunnel != nil && !session.tunnel.IsClose()
	default:
		if strings.HasPrefix(resource, "file:") {
			key := strings.TrimPrefix(resource, "file:")
			mux := session.fileChannels[key]
			return mux != nil && !mux.IsClose()
		}
		return true
	}
}

// selectSession returns one live session for an ID. Primary preserves the
// historical first connection preference; RoundRobin and Random are only
// used when the same VKEY has multiple active connection pairs.
func (s *Bridge) selectSession(id int, resource string) *Client {
	if s == nil {
		return nil
	}
	s.clientsMu.Lock()
	value, ok := s.Client.Load(id)
	if !ok {
		s.clientsMu.Unlock()
		return nil
	}
	root, ok := bridgeClient(value)
	if !ok {
		s.clientsMu.Unlock()
		return nil
	}
	ensureClientSessions(root)
	candidates := make([]*Client, 0, len(root.sessions))
	for _, session := range root.sessions {
		if sessionHasResource(session, resource) {
			candidates = append(candidates, session)
		}
	}
	mode := ClientSelectMode()
	var selected *Client
	switch {
	case len(candidates) == 0:
	case mode == Random:
		selected = candidates[rand.Intn(len(candidates))]
	case mode == RoundRobin:
		index := root.selectSeq.Add(1) - 1
		selected = candidates[index%uint64(len(candidates))]
	default:
		selected = candidates[0]
	}
	s.clientsMu.Unlock()
	return selected
}

func (s *Bridge) sessionForLocked(root *Client, resource, versionText string) *Client {
	if root == nil {
		return nil
	}
	ensureClientSessions(root)
	// MAIN and CHAN/FILE are established as separate authenticated TCP
	// connections. There is no wire-level session identifier in the legacy
	// protocol, so pair a data channel with the newest signal that does not yet
	// have one. Independent sessions must exist even in Primary mode so one
	// VKEY can run multiple transports or multiple NPS connections at once;
	// Primary only controls which complete session carries a new traffic link.
	if resource == "tunnel" || resource == "file" {
		for i := len(root.sessions) - 1; i >= 0; i-- {
			session := root.sessions[i]
			session.mu.Lock()
			hasSignal := session.signal != nil && session.signal.Conn != nil
			available := false
			if resource == "tunnel" {
				available = session.tunnel == nil
			} else {
				available = session.file == nil
			}
			session.mu.Unlock()
			if hasSignal && available {
				return session
			}
		}
	}
	for _, session := range root.sessions {
		session.mu.Lock()
		available := false
		switch resource {
		case "signal":
			available = session.signal == nil || session.signal.Conn == nil
		case "tunnel":
			available = session.tunnel == nil
		case "file":
			available = session.file == nil
		}
		session.mu.Unlock()
		if available {
			return session
		}
	}
	variant := NewClient(nil, nil, nil, versionText)
	variant.parent = root
	root.sessions = append(root.sessions, variant)
	return variant
}

// sessionForFileChannelLocked pairs a keyed file mux with the newest currently
// unused client session. File channels may arrive before MAIN/CHAN during NPC
// startup, so a key can also create a temporary session that the later control
// connection will naturally occupy.
func (s *Bridge) sessionForFileChannelLocked(root *Client, key, versionText string) *Client {
	if root == nil {
		return nil
	}
	ensureClientSessions(root)
	for i := len(root.sessions) - 1; i >= 0; i-- {
		session := root.sessions[i]
		session.mu.Lock()
		mux := session.fileChannels[key]
		available := mux == nil || mux.IsClose()
		session.mu.Unlock()
		if available {
			return session
		}
	}
	variant := NewClient(nil, nil, nil, versionText)
	variant.parent = root
	root.sessions = append(root.sessions, variant)
	return variant
}

// clearFileChannelsLocked detaches all keyed file muxes from a session. The
// caller must hold session.mu; muxes are closed after releasing clientsMu.
func clearFileChannelsLocked(session *Client) []*nps_mux.Mux {
	if session == nil || len(session.fileChannels) == 0 {
		return nil
	}
	muxes := make([]*nps_mux.Mux, 0, len(session.fileChannels))
	for key, mux := range session.fileChannels {
		if mux != nil {
			muxes = append(muxes, mux)
		}
		delete(session.fileChannels, key)
	}
	session.fileChannels = nil
	return muxes
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

// TransportSnapshot returns the concrete public listener that authenticated
// the MAIN connection, for example tcp, tls, ws, wss, kcp or quic.
func (c *Client) TransportSnapshot() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	transport := strings.ToLower(strings.TrimSpace(c.Transport))
	c.mu.Unlock()
	return transport
}

func (c *Client) GetNode() *Node {
	if c == nil {
		return nil
	}
	return &Node{client: c}
}

// RequestClientUpdate asks a connected NPC to execute its built-in updater.
// The client version gate is enforced by the Web controller because the
// bridge only owns the live signal connection.
func (s *Bridge) RequestClientUpdate(id int) error {
	if !beego.AppConfig.DefaultBool("allow_remote_update", true) {
		return errors.New("服务端已全局禁止远程升级")
	}
	if s == nil {
		return errors.New("服务端 Bridge 尚未初始化")
	}
	client := s.selectSession(id, "signal")
	if client == nil {
		return fmt.Errorf("客户端 %d 当前未连接", id)
	}
	client.mu.Lock()
	signal := client.signal
	client.mu.Unlock()
	if signal == nil || signal.Conn == nil {
		return fmt.Errorf("客户端 %d 当前未连接", id)
	}
	client.sendMu.Lock()
	defer client.sendMu.Unlock()
	if _, err := signal.Write([]byte(common.CLIENT_UPDATE)); err != nil {
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

// IsClientConnected reports whether at least one complete MAIN+CHAN session
// is available for the client. It deliberately does not expose the internal
// session tree to the Web layer, which keeps duplicate VKEY connections and
// future transport implementations transparent to callers.
func (s *Bridge) IsClientConnected(id int) bool {
	return s != nil && s.selectSession(id, "online") != nil
}

// SelectedClientVersion returns the negotiated version of one live signal
// session. The selection follows bridge_select_mode so status pages and
// remote-update actions describe the same session family used for traffic.
func (s *Bridge) SelectedClientVersion(id int) string {
	if s == nil {
		return ""
	}
	client := s.selectSession(id, "signal")
	if client == nil {
		return ""
	}
	return client.VersionSnapshot()
}

// SelectedClientTransport returns the transport used by the same live session
// selected for traffic. The listener type is recorded during MAIN
// authentication, so TCP+TLS is distinguishable from plain TCP.
func (s *Bridge) SelectedClientTransport(id int) string {
	if s == nil {
		return ""
	}
	client := s.selectSession(id, "online")
	if client == nil {
		return ""
	}
	if transport := client.TransportSnapshot(); transport != "" {
		return transport
	}
	return strings.ToLower(strings.TrimSpace(s.tunnelType))
}

// ActiveClientTransports returns every distinct transport with a live
// signal/tunnel session. A client normally has one entry, while duplicate VKEY
// connections can expose more than one protocol to the management console.
func (s *Bridge) ActiveClientTransports(id int) []string {
	if s == nil {
		return nil
	}
	s.clientsMu.Lock()
	value, ok := s.Client.Load(id)
	if !ok {
		s.clientsMu.Unlock()
		return nil
	}
	root, ok := bridgeClient(value)
	if !ok {
		s.clientsMu.Unlock()
		return nil
	}
	ensureClientSessions(root)
	seen := make(map[string]struct{})
	transports := make([]string, 0, len(root.sessions))
	for _, session := range root.sessions {
		if session == nil || !sessionHasResource(session, "online") {
			continue
		}
		session.mu.Lock()
		transport := strings.ToLower(strings.TrimSpace(session.Transport))
		session.mu.Unlock()
		if transport == "" {
			transport = strings.ToLower(strings.TrimSpace(s.tunnelType))
		}
		if transport == "" {
			continue
		}
		if _, exists := seen[transport]; exists {
			continue
		}
		seen[transport] = struct{}{}
		transports = append(transports, transport)
	}
	s.clientsMu.Unlock()
	return transports
}

func (s *Bridge) requestClientHealthConfig(id int, db *file.DbUtils) error {
	if s == nil {
		return errors.New("服务端 Bridge 尚未初始化")
	}
	client := s.selectSession(id, "signal")
	if client == nil {
		return fmt.Errorf("客户端 %d 当前未连接", id)
	}
	if !supportsHealthConfig(client.VersionSnapshot()) {
		// Older NPCs intentionally keep their local [health_*] configuration;
		// sending the new flag would corrupt their control stream.
		return nil
	}
	profiles := healthProfilesForClientFromDB(db, id)
	client.mu.Lock()
	signal := client.signal
	client.mu.Unlock()
	if signal == nil || signal.Conn == nil {
		return fmt.Errorf("客户端 %d 当前未连接", id)
	}
	client.sendMu.Lock()
	defer client.sendMu.Unlock()
	if _, err := signal.SendInfo(profiles, common.HEALTH_CONFIG); err != nil {
		return fmt.Errorf("发送健康检查配置失败：%w", err)
	}
	return nil
}

type Bridge struct {
	TunnelPort     int //通信隧道端口
	Client         sync.Map
	p2p            atomic.Pointer[p2pService]
	clientsMu      sync.Mutex
	configMu       sync.Mutex
	Register       sync.Map
	tunnelType     string //bridge type kcp or tcp
	virtualTCP     *tool.VirtualListener
	virtualTLS     *tool.VirtualListener
	virtualWS      *tool.VirtualListener
	virtualWSS     *tool.VirtualListener
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
	s.startVirtualBridgeListeners()
	if s.tunnelType == "kcp" {
		logs.Info("server start, the bridge type is %s, the bridge port is %d", s.tunnelType, s.TunnelPort)
		kcpListener, err := conn.NewKcpListener(net.JoinHostPort(strings.TrimSpace(beego.AppConfig.String("bridge_ip")), beego.AppConfig.String("bridge_port")))
		if err != nil {
			return err
		}
		if err := s.startExtraBridgeTransports(); err != nil {
			_ = kcpListener.Close()
			s.closeVirtualBridgeListeners()
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
		s.closeVirtualBridgeListeners()
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
		tlsListener, tlsErr = connection.GetBridgeTLSListener()
		if tlsErr != nil {
			_ = listener.Close()
			s.closeVirtualBridgeListeners()
			return tlsErr
		}
	}
	if err := s.startExtraBridgeTransports(); err != nil {
		_ = listener.Close()
		if tlsListener != nil {
			_ = tlsListener.Close()
		}
		s.closeVirtualBridgeListeners()
		return err
	}
	if tlsListener != nil {
		go conn.Accept(tlsListener, func(c net.Conn) {
			secure := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{crypt.GetCert()}, MinVersion: tls.VersionTLS12})
			if !s.virtualTLS.Serve(secure) {
				_ = secure.Close()
			}
		})
	}
	go s.ping()
	go conn.Accept(listener, func(c net.Conn) {
		if !s.virtualTCP.Serve(c) {
			_ = c.Close()
		}
	})
	return nil
}

// startVirtualBridgeListeners builds the in-process endpoints used by
// bridge:// targets. External TCP/TLS/WebSocket listeners feed the same
// handlers, so nested access does not open a second public socket or bypass
// Bridge authentication.
func (s *Bridge) startVirtualBridgeListeners() {
	if s == nil {
		return
	}
	s.closeVirtualBridgeListeners()
	s.virtualTCP = tool.NewVirtualListener(nil)
	s.virtualTLS = tool.NewVirtualListener(nil)
	s.virtualWS = tool.NewVirtualListener(nil)
	s.virtualWSS = tool.NewVirtualListener(nil)
	go conn.Accept(s.virtualTCP, func(c net.Conn) { s.cliProcess(conn.NewConn(c), "tcp") })
	go conn.Accept(s.virtualTLS, func(c net.Conn) { s.cliProcess(conn.NewConn(c), "tls") })
	go conn.Accept(s.virtualWS, func(c net.Conn) { s.cliProcess(conn.NewConn(c), "ws") })
	go conn.Accept(s.virtualWSS, func(c net.Conn) { s.cliProcess(conn.NewConn(c), "wss") })
}

func (s *Bridge) closeVirtualBridgeListeners() {
	if s == nil {
		return
	}
	for _, listener := range []*tool.VirtualListener{s.virtualTCP, s.virtualTLS, s.virtualWS, s.virtualWSS} {
		if listener != nil {
			_ = listener.Close()
		}
	}
	s.virtualTCP, s.virtualTLS, s.virtualWS, s.virtualWSS = nil, nil, nil, nil
}

// CloseVirtualBridgeListeners releases in-process Bridge routes when the
// management service is stopped or replaced.
func (s *Bridge) CloseVirtualBridgeListeners() { s.closeVirtualBridgeListeners() }

func (s *Bridge) dialBridgeTarget(target, remote string) (net.Conn, error) {
	if s == nil {
		return nil, errors.New("Bridge is not running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var listener *tool.VirtualListener
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "tcp":
		listener = s.virtualTCP
	case "tls":
		listener = s.virtualTLS
	case "ws":
		listener = s.virtualWS
	case "wss":
		listener = s.virtualWSS
	case "web":
		return tool.GetWebServerConn(ctx, remote)
	case "ipreg":
		return tool.GetIPRegisterConn(ctx, remote)
	default:
		return nil, fmt.Errorf("unsupported bridge target %q", target)
	}
	if listener == nil {
		return nil, errors.New("Bridge virtual listener is not running")
	}
	return listener.Dial(ctx, remote)
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
func (s *Bridge) requestClientLocalAddr(id int, c *conn.Conn) string {
	if c == nil || c.Conn == nil {
		return ""
	}
	if _, err := c.Write([]byte(common.REPORT_LOCAL_IP)); err != nil {
		return ""
	}
	_ = c.Conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer func() { _ = c.Conn.SetReadDeadline(time.Time{}) }()
	b, err := c.GetShortLenContent()
	if err != nil {
		logs.Trace("[兼容] clientId %d 未上报内网地址（旧客户端或超时）：%v", id, err)
		return ""
	}
	localAddr := strings.TrimSpace(string(b))
	if localAddr == "" {
		return ""
	}
	if client, err := file.GetDb().GetClient(id); err == nil {
		client.Lock()
		client.LocalAddr = localAddr
		client.Unlock()
		logs.Info("[客户端] clientId %d 内网地址：%s", id, localAddr)
	}
	return localAddr
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
	sourceAddr := netAddrString(c.Conn.RemoteAddr())
	serverAddr := netAddrString(c.Conn.LocalAddr())
	// Apply the administrator's global source policy before reading the
	// handshake. This protects the bridge/control plane as well as visitor
	// proxy listeners and avoids spending handshake resources on denied peers.
	if file.GetDb().GlobalIPDenied(sourceAddr) {
		logs.Warn("[访问控制] 拒绝桥接来源 %s", c.Conn.RemoteAddr())
		recordBridgeEvent(bridgeEventOptions{
			action:     connectionlog.ActionBridgeFailed,
			result:     connectionlog.ResultFailure,
			stage:      "global_access_control",
			sourceAddr: sourceAddr,
			serverAddr: serverAddr,
			transport:  transportType,
			message:    "global access control denied",
		})
		_ = c.Close()
		return
	}
	// Bound the handshake so half-open/scanning connections do not retain a goroutine.
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	//read test flag
	if _, err := c.GetShortContent(3); err != nil {
		logs.Info("[连接异常] 客户端 %s 握手失败：%s", c.Conn.RemoteAddr(), err.Error())
		recordBridgeEvent(bridgeEventOptions{
			action:     connectionlog.ActionBridgeFailed,
			result:     connectionlog.ResultFailure,
			stage:      "handshake",
			sourceAddr: sourceAddr,
			serverAddr: serverAddr,
			transport:  transportType,
			message:    err.Error(),
		})
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
		recordBridgeEvent(bridgeEventOptions{
			action:     connectionlog.ActionBridgeFailed,
			result:     connectionlog.ResultFailure,
			stage:      "client_version",
			sourceAddr: sourceAddr,
			serverAddr: serverAddr,
			transport:  transportType,
			message:    err.Error(),
		})
		c.Close()
		return
	}
	clientVersion := strings.TrimSpace(strings.Trim(string(vs), "\x00"))
	id, err := s.authenticateClient(c, clientVersion, file.GetDb())
	if err != nil {
		logs.Info("[验证失败] 拒绝客户端连接 %s，版本 %q，原因：%v", c.Conn.RemoteAddr(), clientVersion, err)
		recordBridgeEvent(bridgeEventOptions{
			clientID:      id,
			action:        connectionlog.ActionBridgeAuthFailed,
			result:        connectionlog.ResultFailure,
			stage:         "authentication",
			sourceAddr:    sourceAddr,
			serverAddr:    serverAddr,
			clientVersion: clientVersion,
			transport:     transportType,
			message:       err.Error(),
		})
		_ = c.Close()
		return
	}
	if flag, err := c.ReadFlag(); err == nil {
		_ = c.SetReadDeadline(time.Time{})
		s.typeDeal(flag, c, id, clientVersion, transportType)
	} else {
		logs.Warn("[连接异常] 读取客户端连接类型失败：%v，flag=%q", err, flag)
		recordBridgeEvent(bridgeEventOptions{
			clientID:      id,
			action:        connectionlog.ActionBridgeFailed,
			result:        connectionlog.ResultFailure,
			stage:         "connection_type",
			sourceAddr:    sourceAddr,
			serverAddr:    serverAddr,
			clientVersion: clientVersion,
			transport:     transportType,
			message:       err.Error(),
		})
		c.Close()
	}
	return
}

// ServeQUICConn accepts one already-negotiated QUIC connection as a Bridge
// transport. HTTP/3 sharing uses this entry point after ALPN/SNI dispatch;
// the first bidirectional stream carries the ordinary NPS handshake.
func (s *Bridge) ServeQUICConn(qc *quic.Conn, handshakeTimeout time.Duration, transport ...string) {
	if s == nil || qc == nil {
		if qc != nil {
			_ = qc.CloseWithError(0, "invalid bridge")
		}
		return
	}
	if handshakeTimeout <= 0 {
		handshakeTimeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(qc.Context(), handshakeTimeout)
	stream, err := qc.AcceptStream(ctx)
	cancel()
	if err != nil {
		_ = qc.CloseWithError(0, "bridge stream timeout")
		return
	}
	transportType := "quic"
	if len(transport) > 0 && strings.TrimSpace(transport[0]) != "" {
		transportType = transport[0]
	}
	go s.cliProcess(conn.NewConn(conn.NewQuicAutoCloseConn(stream, qc)), transportType)
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
	if err := crypt.VerifySecureHandshakeClientPayload(verifyKey, version.GetVersion(), clientVersion, payload, challenge, common.TimeNow().Unix(), window); err != nil {
		return 0, err
	}
	if !db.IsClientActiveWithExpiry(clientRecord, beego.AppConfig.DefaultBool("allow_time_limit", true)) {
		return clientRecord.Id, errors.New("客户端已停用或已过期")
	}
	clientRecord.Lock()
	if clientRecord.VerifyKey != verifyKey || !clientRecord.Status {
		clientID := clientRecord.Id
		clientRecord.Unlock()
		return clientID, errors.New("client credentials changed during handshake")
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
	if version.SupportsModernP2P(clientVersion) {
		crypt.EnsureTls()
		fingerprint := crypt.GetCertificateFingerprint(crypt.GetCert())
		if err := c.WriteLenContent(fingerprint); err != nil {
			return 0, fmt.Errorf("write bridge fingerprint: %w", err)
		}
		proof, err := crypt.SecureHandshakeFingerprintProof(verifyKey, fingerprint)
		if err != nil {
			return 0, err
		}
		if _, err := c.Write(proof); err != nil {
			return 0, fmt.Errorf("write bridge fingerprint proof: %w", err)
		}
	}
	return clientID, nil
}

func (s *Bridge) DelClient(id int) {
	s.delClient(id, nil)
}

// KickClient tells a connected NPC that its registration has been removed
// and then tears down every live session. Delivering the flag is best-effort:
// NPCs predating this feature simply observe the close and keep their
// historical reconnect behaviour.
func (s *Bridge) KickClient(id int) {
	if s == nil {
		return
	}
	s.writeKickFlag(id)
	s.delClient(id, nil)
}

// writeKickFlag serializes one CLIENT_CLOSE frame per active MAIN signal so
// it cannot interleave with other Bridge-initiated control frames.
func (s *Bridge) writeKickFlag(id int) {
	s.clientsMu.Lock()
	v, ok := s.Client.Load(id)
	if !ok {
		s.clientsMu.Unlock()
		return
	}
	root, valid := bridgeClient(v)
	if !valid {
		s.clientsMu.Unlock()
		return
	}
	ensureClientSessions(root)
	type target struct {
		cl     *Client
		signal *conn.Conn
	}
	targets := make([]target, 0, len(root.sessions))
	for _, cl := range append([]*Client(nil), root.sessions...) {
		if cl == nil {
			continue
		}
		cl.mu.Lock()
		if cl.signal != nil {
			targets = append(targets, target{cl: cl, signal: cl.signal})
		}
		cl.mu.Unlock()
	}
	s.clientsMu.Unlock()

	// Web deletion must stay fast: cap the whole best-effort flush at one
	// second across every signal instead of allowing a stale control
	// connection to consume the budget per signal.
	deadline := time.Now().Add(time.Second)
	for _, tgt := range targets {
		if time.Now().After(deadline) {
			break
		}
		if tgt.signal == nil || tgt.signal.Conn == nil {
			continue
		}
		// The frame is best-effort and must never stall Bridge-wide cleanup
		// on an unresponsive control connection.
		tgt.cl.sendMu.Lock()
		_ = tgt.signal.Conn.SetWriteDeadline(deadline)
		_, _ = tgt.signal.Write([]byte(common.CLIENT_CLOSE))
		_ = tgt.signal.Conn.SetWriteDeadline(time.Time{})
		tgt.cl.sendMu.Unlock()
	}
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
	root, ok := bridgeClient(v)
	if !ok {
		s.clientsMu.Unlock()
		return
	}
	ensureClientSessions(root)
	cl := findClientSession(root, snapshot.client)
	if cl == nil {
		s.clientsMu.Unlock()
		return
	}
	cl.mu.Lock()
	if cl.signal != snapshot.signal || cl.tunnel != snapshot.tunnel || cl.file != snapshot.file {
		cl.mu.Unlock()
		s.clientsMu.Unlock()
		return
	}
	signal, tunnel, fileMux := cl.signal, cl.tunnel, cl.file
	cl.signal, cl.tunnel, cl.file = nil, nil, nil
	remainingFiles := clearFileChannelsLocked(cl)
	cl.mu.Unlock()
	removeClientSession(root, cl)
	remainingSignals := make([]*conn.Conn, 0)
	remainingTunnels := make([]*nps_mux.Mux, 0)
	clientOffline := false
	if !clientHasSignal(root) {
		for _, remaining := range append([]*Client(nil), root.sessions...) {
			if remaining == nil {
				continue
			}
			remaining.mu.Lock()
			if remaining.signal != nil {
				remainingSignals = append(remainingSignals, remaining.signal)
			}
			if remaining.tunnel != nil {
				remainingTunnels = append(remainingTunnels, remaining.tunnel)
			}
			if remaining.file != nil {
				remainingFiles = append(remainingFiles, remaining.file)
			}
			remainingFiles = append(remainingFiles, clearFileChannelsLocked(remaining)...)
			remaining.signal, remaining.tunnel, remaining.file = nil, nil, nil
			remaining.mu.Unlock()
		}
		root.sessions = nil
		s.recordClientConnection(id, false)
		s.Client.Delete(id)
		clientOffline = true
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
	for _, remaining := range remainingSignals {
		if remaining != nil {
			_ = remaining.Close()
		}
	}
	for _, remaining := range remainingTunnels {
		if remaining != nil {
			_ = remaining.Close()
		}
	}
	for _, remaining := range remainingFiles {
		if remaining != nil {
			_ = remaining.Close()
		}
	}
	if !clientOffline || file.GetDb().IsPubClient(id) {
		return
	}
	if c, err := file.GetDb().GetClient(id); err == nil {
		select {
		case s.CloseClient <- c.Id:
		default:
		}
	}
}

func findClientSession(root, wanted *Client) *Client {
	if root == nil || wanted == nil {
		return nil
	}
	ensureClientSessions(root)
	for _, session := range root.sessions {
		if session == wanted {
			return session
		}
	}
	return nil
}

func findClientSessionBySignal(root *Client, signal *conn.Conn) *Client {
	if root == nil || signal == nil {
		return nil
	}
	ensureClientSessions(root)
	for _, session := range root.sessions {
		session.mu.Lock()
		match := session.signal == signal
		session.mu.Unlock()
		if match {
			return session
		}
	}
	return nil
}

func removeClientSession(root, target *Client) {
	if root == nil || target == nil || target == root {
		return
	}
	for i, session := range root.sessions {
		if session == target {
			root.sessions = append(root.sessions[:i], root.sessions[i+1:]...)
			return
		}
	}
}

func clientHasSignal(root *Client) bool {
	if root == nil {
		return false
	}
	ensureClientSessions(root)
	for _, session := range root.sessions {
		session.mu.Lock()
		connected := session.signal != nil && session.signal.Conn != nil
		session.mu.Unlock()
		if connected {
			return true
		}
	}
	return false
}

// delClient serializes a main-connection replacement with cleanup. When
// expectedSignal is set, the delete applies only if that signal is still the
// active one for the client.
func (s *Bridge) delClient(id int, expectedSignal *conn.Conn) {
	var signals []*conn.Conn
	var tunnels []*nps_mux.Mux
	var files []*nps_mux.Mux
	clientOffline := false
	s.clientsMu.Lock()
	v, ok := s.Client.Load(id)
	if !ok {
		s.clientsMu.Unlock()
		return
	}
	root, valid := bridgeClient(v)
	if !valid {
		// A malformed runtime entry must not crash the cleanup goroutine.
		s.Client.Delete(id)
		s.clientsMu.Unlock()
		return
	}
	ensureClientSessions(root)
	if expectedSignal == nil {
		// DelClient is an explicit administrative disconnect. A root-only
		// cleanup would leave RoundRobin/Random variants alive, allowing the
		// NPC to keep forwarding traffic after it was removed from the panel.
		for _, cl := range append([]*Client(nil), root.sessions...) {
			if cl == nil {
				continue
			}
			cl.mu.Lock()
			if cl.signal != nil {
				signals = append(signals, cl.signal)
			}
			if cl.tunnel != nil {
				tunnels = append(tunnels, cl.tunnel)
			}
			if cl.file != nil {
				files = append(files, cl.file)
			}
			files = append(files, clearFileChannelsLocked(cl)...)
			cl.signal, cl.tunnel, cl.file = nil, nil, nil
			cl.mu.Unlock()
		}
		root.sessions = nil
		s.Client.Delete(id)
		s.recordClientConnection(id, false)
		clientOffline = true
	} else {
		cl := findClientSessionBySignal(root, expectedSignal)
		if cl == nil {
			s.clientsMu.Unlock()
			return
		}
		cl.mu.Lock()
		if cl.signal != nil {
			signals = append(signals, cl.signal)
		}
		if cl.tunnel != nil {
			tunnels = append(tunnels, cl.tunnel)
		}
		if cl.file != nil {
			files = append(files, cl.file)
		}
		files = append(files, clearFileChannelsLocked(cl)...)
		cl.signal, cl.tunnel, cl.file = nil, nil, nil
		cl.mu.Unlock()
		removeClientSession(root, cl)
		if !clientHasSignal(root) {
			// A malformed or out-of-order reconnect can leave a data-only
			// variant behind. Once the last MAIN is gone, close every remaining
			// resource before deleting the map entry as well.
			for _, remaining := range append([]*Client(nil), root.sessions...) {
				if remaining == nil {
					continue
				}
				remaining.mu.Lock()
				if remaining.signal != nil {
					signals = append(signals, remaining.signal)
				}
				if remaining.tunnel != nil {
					tunnels = append(tunnels, remaining.tunnel)
				}
				if remaining.file != nil {
					files = append(files, remaining.file)
				}
				files = append(files, clearFileChannelsLocked(remaining)...)
				remaining.signal, remaining.tunnel, remaining.file = nil, nil, nil
				remaining.mu.Unlock()
			}
			root.sessions = nil
			s.recordClientConnection(id, false)
			s.Client.Delete(id)
			clientOffline = true
		}
	}
	s.clientsMu.Unlock()

	for _, signal := range signals {
		if signal != nil {
			_ = signal.Close()
		}
	}
	for _, tunnel := range tunnels {
		if tunnel != nil {
			_ = tunnel.Close()
		}
	}
	for _, fileMux := range files {
		if fileMux != nil {
			_ = fileMux.Close()
		}
	}
	if !clientOffline || file.GetDb().IsPubClient(id) {
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
		if !online {
			client.RLock()
			sourceAddr, clientVersion := client.Addr, client.Version
			client.RUnlock()
			recordBridgeEvent(bridgeEventOptions{
				clientID:      id,
				action:        connectionlog.ActionBridgeDisconnected,
				result:        connectionlog.ResultSuccess,
				channel:       common.WORK_MAIN,
				stage:         "disconnect",
				sourceAddr:    sourceAddr,
				clientVersion: clientVersion,
			})
		}
	}
}

type bridgeEventOptions struct {
	clientID          int
	action            string
	result            string
	channel           string
	stage             string
	sourceAddr        string
	serverAddr        string
	clientVersion     string
	transport         string
	clientLocalAddr   string
	clientLocalSource string
	message           string
}

func recordBridgeChannelEvent(c *conn.Conn, clientID int, result, clientVersion, transport, channel, stage, message string) {
	options := bridgeEventOptions{
		clientID:      clientID,
		action:        connectionlog.ActionBridgeChannel,
		result:        result,
		channel:       channel,
		stage:         stage,
		clientVersion: clientVersion,
		transport:     transport,
		message:       message,
	}
	if c != nil && c.Conn != nil {
		options.sourceAddr = netAddrString(c.Conn.RemoteAddr())
		options.serverAddr = netAddrString(c.Conn.LocalAddr())
	}
	recordBridgeEvent(options)
}

func recordBridgeEvent(options bridgeEventOptions) {
	action := strings.TrimSpace(options.action)
	result := strings.TrimSpace(options.result)
	if action == "" {
		if result == connectionlog.ResultFailure {
			action = connectionlog.ActionBridgeAuthFailed
		} else {
			action = connectionlog.ActionBridgeConnected
		}
	}
	if result == "" {
		result = connectionlog.ResultSuccess
	}
	sourceAddr := strings.TrimSpace(options.sourceAddr)
	event := connectionlog.Event{
		Kind:                  connectionlog.KindBridge,
		Action:                action,
		Result:                result,
		ClientID:              options.clientID,
		ResourceType:          "client",
		ResourceID:            options.clientID,
		BridgeChannel:         normalizeBridgeChannel(options.channel),
		SourceIP:              common.GetIpByAddr(sourceAddr),
		RemoteAddr:            sourceAddr,
		ServerAddr:            strings.TrimSpace(options.serverAddr),
		ClientLocalAddr:       strings.TrimSpace(options.clientLocalAddr),
		ClientLocalAddrSource: strings.TrimSpace(options.clientLocalSource),
		ClientVersion:         strings.TrimSpace(options.clientVersion),
		HandshakeStage:        strings.TrimSpace(options.stage),
		Error:                 options.message,
	}
	if strings.TrimSpace(options.transport) != "" {
		event.BridgeTransport = normalizeBridgeTransport(options.transport)
	}
	if options.clientID > 0 {
		if client, err := file.GetDb().GetClient(options.clientID); err == nil && client != nil {
			client.RLock()
			event.UserID = client.UserId
			event.UserName = client.UserName
			event.ClientRemark = client.Remark
			if event.SourceIP == "" {
				event.SourceIP = common.GetIpByAddr(client.Addr)
			}
			if event.RemoteAddr == "" {
				event.RemoteAddr = strings.TrimSpace(client.Addr)
			}
			if event.ClientVersion == "" {
				event.ClientVersion = client.Version
			}
			event.ClientPublicIP = common.GetIpByAddr(client.Addr)
			event.ClientStatus = enabledStatus(client.Status)
			event.ClientExpireTime = strings.TrimSpace(client.ExpireTime)
			event.ClientLastOnlineTime = strings.TrimSpace(client.LastOnlineTime)
			if event.ClientLocalAddr == "" {
				event.ClientLocalAddr = strings.TrimSpace(client.LocalAddr)
				if event.ClientLocalAddr != "" {
					event.ClientLocalAddrSource = connectionlog.ClientLocalAddrSourceClientRecord
					if action == connectionlog.ActionBridgeAuthFailed {
						event.ClientLocalAddrSource = connectionlog.ClientLocalAddrSourceLastSuccessful
					}
				}
			}
			client.RUnlock()
			if effectiveExpire, expireErr := file.GetDb().EffectiveClientExpireTime(client); expireErr == nil {
				event.ClientEffectiveExpireTime = effectiveExpire
			}
			if event.UserID > 0 {
				if user, userErr := file.GetDb().GetUser(event.UserID); userErr == nil && user != nil {
					user.RLock()
					if event.UserName == "" {
						event.UserName = user.UserName
					}
					event.UserStatus = enabledStatus(user.Status)
					event.UserExpireTime = strings.TrimSpace(user.ExpireTime)
					user.RUnlock()
				}
			}
		}
	}
	if err := connectionlog.Append(event); err != nil {
		logs.Warn("写入连接日志失败: %v", err)
	}
}

func netAddrString(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	return strings.TrimSpace(addr.String())
}

func enabledStatus(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func normalizeBridgeChannel(channel string) string {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case common.WORK_MAIN:
		return "main"
	case common.WORK_CHAN:
		return "data"
	case common.WORK_CONFIG:
		return "config"
	case common.WORK_REGISTER:
		return "register"
	case common.WORK_SECRET:
		return "secret"
	case common.WORK_FILE:
		return "file"
	case common.WORK_P2P, "p2p":
		return "p2p"
	case common.WORK_P2P_SESSION, "p2p_session":
		return "p2p_session"
	default:
		return strings.ToLower(strings.TrimSpace(channel))
	}
}

func normalizeBridgeTransport(transport string) string {
	switch strings.ToLower(strings.TrimSpace(transport)) {
	case "tcp", "tls", "kcp", "ws", "wss", "quic", "http3":
		return strings.ToLower(strings.TrimSpace(transport))
	default:
		return "tcp"
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
			recordBridgeChannelEvent(c, id, connectionlog.ResultFailure, vs, transportType,
				common.WORK_MAIN, "channel_rejected", "公共客户端不能建立主控制连接")
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
		var selected *Client
		s.clientsMu.Lock()
		wasOnline := false
		if v, ok := s.Client.Load(id); ok {
			if cl, valid := bridgeClient(v); valid {
				ensureClientSessions(cl)
				for _, session := range cl.sessions {
					session.mu.Lock()
					if session.signal != nil {
						wasOnline = true
					}
					session.mu.Unlock()
				}
				selected = s.sessionForLocked(cl, "signal", vs)
				selected.mu.Lock()
				oldSignal = selected.signal
				selected.signal = c
				selected.Version = vs
				selected.Transport = normalizeBridgeTransport(transportType)
				selected.mu.Unlock()
			} else {
				s.Client.Delete(id)
				selected = NewClient(nil, nil, c, vs)
				selected.Transport = normalizeBridgeTransport(transportType)
				ensureClientSessions(selected)
				s.Client.Store(id, selected)
			}
		} else {
			selected = NewClient(nil, nil, c, vs)
			selected.Transport = normalizeBridgeTransport(transportType)
			ensureClientSessions(selected)
			s.Client.Store(id, selected)
		}
		if !wasOnline {
			s.recordClientConnection(id, true)
		}
		s.clientsMu.Unlock()
		if oldSignal != nil && oldSignal != c {
			_ = oldSignal.Close()
		}
		clientLocalAddr := s.requestClientLocalAddr(id, c)
		clientLocalAddrSource := ""
		if clientLocalAddr != "" {
			clientLocalAddrSource = connectionlog.ClientLocalAddrSourceCurrentReport
		}
		recordBridgeEvent(bridgeEventOptions{
			clientID:          id,
			action:            connectionlog.ActionBridgeConnected,
			result:            connectionlog.ResultSuccess,
			channel:           common.WORK_MAIN,
			stage:             "connected",
			sourceAddr:        netAddrString(c.Conn.RemoteAddr()),
			serverAddr:        netAddrString(c.Conn.LocalAddr()),
			clientVersion:     vs,
			transport:         transportType,
			clientLocalAddr:   clientLocalAddr,
			clientLocalSource: clientLocalAddrSource,
		})
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
				selected := s.sessionForLocked(cl, "tunnel", vs)
				selected.mu.Lock()
				oldTunnel = selected.tunnel
				selected.tunnel = muxConn
				if selected.Version == "" {
					selected.Version = vs
				}
				if selected.Transport == "" {
					selected.Transport = normalizeBridgeTransport(transportType)
				}
				selected.mu.Unlock()
			} else {
				s.Client.Delete(id)
				replacement := NewClient(muxConn, nil, nil, vs)
				replacement.Transport = normalizeBridgeTransport(transportType)
				ensureClientSessions(replacement)
				s.Client.Store(id, replacement)
			}
		} else {
			cl := NewClient(muxConn, nil, nil, vs)
			cl.Transport = normalizeBridgeTransport(transportType)
			ensureClientSessions(cl)
			s.Client.Store(id, cl)
		}
		s.clientsMu.Unlock()
		if oldTunnel != nil {
			_ = oldTunnel.Close()
		}
		if version.SupportsModernP2P(vs) && s.p2p.Load() != nil {
			go s.acceptModernP2PStreams(id, vs, transportType, muxConn)
		}
	case common.WORK_CONFIG:
		client, err := file.GetDb().GetClient(id)
		if err != nil || client == nil {
			recordBridgeChannelEvent(c, id, connectionlog.ResultFailure, vs, transportType,
				common.WORK_CONFIG, "channel_setup", "客户端记录不存在")
			c.Close()
			return
		}
		client.RLock()
		configConnAllow := client.ConfigConnAllow
		client.RUnlock()
		if !isPub && !configConnAllow {
			recordBridgeChannelEvent(c, id, connectionlog.ResultFailure, vs, transportType,
				common.WORK_CONFIG, "channel_rejected", "客户端未启用配置通道")
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
			recordBridgeChannelEvent(c, id, connectionlog.ResultFailure, vs, transportType,
				common.WORK_SECRET, "channel_setup", err.Error())
			c.Close()
		}
	case common.WORK_FILE:
		if version.SupportsKeyedFiles(vs) {
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			payload, err := c.GetShortLenContent()
			_ = c.SetReadDeadline(time.Time{})
			key, valid := file.ParseFileAccessTarget(file.FileAccessTargetPrefix + string(payload))
			if err != nil || !valid || !fileAccessKeyOwnedByClient(id, key) {
				logs.Warn("[文件访问] clientId %d 提交了无效或未授权的文件通道键", id)
				message := "文件通道键无效或不属于该客户端"
				if err != nil {
					message = err.Error()
				}
				recordBridgeChannelEvent(c, id, connectionlog.ResultFailure, vs, transportType,
					common.WORK_FILE, "channel_setup", message)
				_ = c.Close()
				return
			}
			muxConn := nps_mux.NewMux(c.Conn, transportType, s.disconnectTime)
			var oldFile *nps_mux.Mux
			s.clientsMu.Lock()
			if v, ok := s.Client.Load(id); ok {
				if root, valid := bridgeClient(v); valid {
					selected := s.sessionForFileChannelLocked(root, key, vs)
					selected.mu.Lock()
					if selected.fileChannels == nil {
						selected.fileChannels = make(map[string]*nps_mux.Mux)
					}
					oldFile = selected.fileChannels[key]
					selected.fileChannels[key] = muxConn
					if selected.Version == "" {
						selected.Version = vs
					}
					selected.mu.Unlock()
				} else {
					s.Client.Delete(id)
					replacement := NewClient(nil, muxConn, nil, vs)
					replacement.fileChannels = map[string]*nps_mux.Mux{key: muxConn}
					ensureClientSessions(replacement)
					s.Client.Store(id, replacement)
				}
			} else {
				root := NewClient(nil, nil, nil, vs)
				root.fileChannels = map[string]*nps_mux.Mux{key: muxConn}
				ensureClientSessions(root)
				s.Client.Store(id, root)
			}
			s.clientsMu.Unlock()
			if oldFile != nil && oldFile != muxConn {
				_ = oldFile.Close()
			}
			recordBridgeChannelEvent(c, id, connectionlog.ResultSuccess, vs, transportType,
				common.WORK_FILE, "channel_ready", "")
			return
		}
		muxConn := nps_mux.NewMux(c.Conn, transportType, s.disconnectTime)
		var oldFile *nps_mux.Mux
		s.clientsMu.Lock()
		if v, ok := s.Client.Load(id); ok {
			if cl, valid := bridgeClient(v); valid {
				selected := s.sessionForLocked(cl, "file", vs)
				selected.mu.Lock()
				oldFile = selected.file
				selected.file = muxConn
				if selected.Version == "" {
					selected.Version = vs
				}
				selected.mu.Unlock()
			} else {
				s.Client.Delete(id)
				replacement := NewClient(nil, muxConn, nil, vs)
				ensureClientSessions(replacement)
				s.Client.Store(id, replacement)
			}
		} else {
			cl := NewClient(nil, muxConn, nil, vs)
			ensureClientSessions(cl)
			s.Client.Store(id, cl)
		}
		s.clientsMu.Unlock()
		if oldFile != nil {
			_ = oldFile.Close()
		}
	case common.WORK_P2P:
		// NPC 1.1.29+ sends the modern JSON control request. Older clients
		// continue through the historical UDP/KCP handshake below.
		if version.SupportsModernP2P(vs) && s.p2p.Load() != nil {
			recordBridgeChannelEvent(c, id, connectionlog.ResultSuccess, vs, transportType,
				common.WORK_P2P, "channel_ready", "")
			go s.handleP2PVisitor(c, id, 0, vs, "", transportType)
			return
		}
		//read md5 secret
		if b, err := c.GetShortContent(32); err != nil {
			logs.Error("p2p error,", err.Error())
			recordBridgeChannelEvent(c, id, connectionlog.ResultFailure, vs, transportType,
				common.WORK_P2P, "channel_setup", err.Error())
			c.Close()
		} else if t := file.GetDb().GetTaskByMd5Password(string(b)); t == nil {
			logs.Error("p2p error, failed to match the key successfully")
			recordBridgeChannelEvent(c, id, connectionlog.ResultFailure, vs, transportType,
				common.WORK_P2P, "channel_setup", "P2P 任务密钥不存在")
			c.Close()
		} else {
			t.RLock()
			taskStatus, taskMode, taskClient := t.Status, t.Mode, t.Client
			t.RUnlock()
			if !taskStatus || taskMode != "p2p" || taskClient == nil {
				logs.Error("p2p error, task is inactive or malformed")
				recordBridgeChannelEvent(c, id, connectionlog.ResultFailure, vs, transportType,
					common.WORK_P2P, "channel_rejected", "P2P 任务已停用或数据无效")
				c.Close()
				return
			}
			taskClient.RLock()
			clientID := taskClient.Id
			taskClient.RUnlock()
			if cl := s.selectSession(clientID, "signal"); cl == nil {
				recordBridgeChannelEvent(c, id, connectionlog.ResultFailure, vs, transportType,
					common.WORK_P2P, "channel_setup", "P2P 提供端未在线")
				c.Close()
				return
			} else {
				cl.mu.Lock()
				sig := cl.signal
				cl.mu.Unlock()
				if sig == nil {
					logs.Error("p2p error, client signal is nil")
					recordBridgeChannelEvent(c, id, connectionlog.ResultFailure, vs, transportType,
						common.WORK_P2P, "channel_setup", "P2P 提供端控制连接不可用")
					c.Close()
					return
				}
				if _, err := sig.Write([]byte(common.NEW_UDP_CONN)); err != nil {
					recordBridgeChannelEvent(c, id, connectionlog.ResultFailure, vs, transportType,
						common.WORK_P2P, "channel_setup", err.Error())
					_ = c.Close()
					return
				}
				svrAddr := beego.AppConfig.String("p2p_ip") + ":" + beego.AppConfig.String("p2p_port")
				if err := sig.WriteLenContent([]byte(svrAddr)); err != nil {
					recordBridgeChannelEvent(c, id, connectionlog.ResultFailure, vs, transportType,
						common.WORK_P2P, "channel_setup", err.Error())
					_ = c.Close()
					return
				}
				if err := sig.WriteLenContent(b); err != nil {
					recordBridgeChannelEvent(c, id, connectionlog.ResultFailure, vs, transportType,
						common.WORK_P2P, "channel_setup", err.Error())
					_ = c.Close()
					return
				}
				if err := c.WriteLenContent([]byte(svrAddr)); err != nil {
					recordBridgeChannelEvent(c, id, connectionlog.ResultFailure, vs, transportType,
						common.WORK_P2P, "channel_setup", err.Error())
					_ = c.Close()
					return
				}
				recordBridgeChannelEvent(c, id, connectionlog.ResultSuccess, vs, transportType,
					common.WORK_P2P, "channel_ready", "")
			}
		}
		return
	case common.WORK_P2P_SESSION:
		if version.SupportsModernP2P(vs) && s.p2p.Load() != nil {
			recordBridgeChannelEvent(c, id, connectionlog.ResultSuccess, vs, transportType,
				common.WORK_P2P_SESSION, "channel_ready", "")
			go s.handleP2PJoin(c, id, 0, vs, transportType)
			return
		}
		recordBridgeChannelEvent(c, id, connectionlog.ResultFailure, vs, transportType,
			common.WORK_P2P_SESSION, "channel_rejected", "当前连接不支持新版 P2P")
		_ = c.Close()
		return
	}
	if typeVal != common.WORK_MAIN {
		recordBridgeEvent(bridgeEventOptions{
			clientID:      id,
			action:        connectionlog.ActionBridgeChannel,
			result:        connectionlog.ResultSuccess,
			channel:       typeVal,
			stage:         "channel_ready",
			sourceAddr:    netAddrString(c.Conn.RemoteAddr()),
			serverAddr:    netAddrString(c.Conn.LocalAddr()),
			clientVersion: vs,
			transport:     transportType,
		})
	}
	c.SetAlive(transportType)
	return
}

// acceptModernP2PStreams handles provider-initiated control streams carried by
// the authenticated client mux. They are intentionally kept separate from
// ordinary proxy streams: the modern P2P protocol writes only a four-byte work
// type followed by its JSON envelope, not a second Bridge authentication.
func (s *Bridge) acceptModernP2PStreams(id int, vs, transport string, muxConn *nps_mux.Mux) {
	if muxConn == nil {
		return
	}
	for {
		stream, err := muxConn.Accept()
		if err != nil {
			return
		}
		go func(stream net.Conn) {
			c := conn.NewConn(stream)
			flag, err := c.ReadFlag()
			if err != nil {
				_ = c.Close()
				return
			}
			switch flag {
			case common.WORK_P2P_SESSION:
				s.handleP2PJoin(c, id, 0, vs, transport)
			default:
				_ = c.Close()
			}
		}(stream)
	}
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
	if key, isFileTarget := file.ParseFileAccessTarget(link.Host); isFileTarget {
		if !fileAccessKeyOwnedByClient(clientId, key) {
			return nil, errors.New("file access target is not owned by this client")
		}
		clientRecord, _ := file.GetDb().GetClient(clientId)
		if registeredIPRequired(clientRecord, t) && !ipreg.RegisteredString(clientId, link.RemoteAddr) {
			return nil, fmt.Errorf("访问 IP %s 尚未完成登记", common.GetIpByAddr(link.RemoteAddr))
		}
		if s.ipVerify {
			ip := common.GetIpByAddr(link.RemoteAddr)
			registered, ok := s.Register.Load(ip)
			expiresAt, valid := registered.(time.Time)
			if !ok || !valid || !expiresAt.After(time.Now()) {
				return nil, fmt.Errorf("The ip %s is not in the validation list", ip)
			}
		}
		selected := s.selectSession(clientId, "file:"+key)
		var mux *nps_mux.Mux
		if selected != nil {
			selected.mu.Lock()
			mux = selected.fileChannels[key]
			selected.mu.Unlock()
		}
		// Older NPC releases do not send a keyed file-channel preamble. Keep
		// their existing single file mux usable during the compatibility window.
		if mux == nil || mux.IsClose() {
			selected = s.selectSession(clientId, "file")
			if selected != nil {
				selected.mu.Lock()
				mux = selected.file
				selected.mu.Unlock()
			}
		}
		if mux == nil || mux.IsClose() {
			return nil, errors.New("the client file service is not connected")
		}
		link.Crypt, link.Compress = false, false
		return mux.NewConn()
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(link.Host)), file.FileAccessTargetPrefix) {
		return nil, errors.New("invalid file access target key")
	}
	if scheme, value, ok := strings.Cut(strings.TrimSpace(link.Host), "://"); ok && strings.EqualFold(scheme, "bridge") {
		if strings.TrimSpace(value) == "" {
			return nil, errors.New("bridge:// target is empty")
		}
		if clientId >= 0 && (!link.LocalProxy || !bridgeAllowsLocalProxy(clientId)) {
			return nil, errors.New("bridge:// requires administrator-enabled server-local proxy access")
		}
		if !bridgeTargetOwnedByAdministrator(clientId) {
			return nil, errors.New("bridge:// targets are reserved for administrator-owned resources")
		}
		link.Crypt, link.Compress = false, false
		return s.dialBridgeTarget(value, link.RemoteAddr)
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(link.Host)), "bridge://") {
		return nil, errors.New("invalid bridge:// target")
	}
	if scheme, value, ok := strings.Cut(strings.TrimSpace(link.Host), "://"); ok && strings.EqualFold(scheme, "tunnel") {
		if !link.LocalProxy || !bridgeAllowsLocalProxy(clientId) {
			return nil, errors.New("tunnel:// requires server-local proxy permission")
		}
		targetID, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil || targetID <= 0 {
			return nil, errors.New("invalid tunnel:// target ID")
		}
		if t != nil && t.Id > 0 && t.Id == targetID {
			return nil, fmt.Errorf("task %d cannot connect to itself (tunnel://%d)", t.Id, targetID)
		}
		if !nestedTunnelOwnerAllowed(clientId, targetID) {
			return nil, errors.New("nested tunnel target is outside the client's tenant")
		}
		link.Crypt, link.Compress = false, false
		return tool.GetTunnelConn(targetID, link.RemoteAddr)
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(link.Host)), "tunnel://") {
		return nil, errors.New("invalid tunnel:// target ID")
	}
	//if the proxy type is local
	if link.LocalProxy {
		if !bridgeAllowsLocalProxy(clientId) {
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
	resource := "tunnel"
	if t != nil && t.Mode == "file" {
		resource = "file"
	}
	if cl := s.selectSession(clientId, resource); cl != nil {
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

func bridgeAllowsLocalProxy(clientID int) bool {
	if clientID < 0 {
		return true
	}
	client, _ := file.GetDb().GetClient(clientID)
	return client.LocalProxyAllowed(
		beego.AppConfig.DefaultBool("allow_local_proxy", false),
		beego.AppConfig.DefaultBool("allow_user_local", false),
	)
}

// bridgeTargetOwnedByAdministrator is a runtime defense in depth for
// bridge:// routes. The Web controller also rejects these targets for normal
// users, but persisted/imported data must not let a tenant reach the server's
// Bridge control plane or management panel.
func bridgeTargetOwnedByAdministrator(clientID int) bool {
	if clientID < 0 {
		return true
	}
	client, err := file.GetDb().GetClient(clientID)
	if err != nil || client == nil {
		return false
	}
	client.RLock()
	userID := client.UserId
	client.RUnlock()
	return userID <= 0
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
				root, ok := value.(*Client)
				if !ok || root == nil {
					return true
				}
				s.clientsMu.Lock()
				ensureClientSessions(root)
				sessions := append([]*Client(nil), root.sessions...)
				s.clientsMu.Unlock()
				for _, v := range sessions {
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
						v.retryTime.Store(0)
						continue
					}
					if snapshot.tunnel == nil || snapshot.signal == nil {
						v.retryTime.Add(1)
						if v.retryTime.Load() >= 3 {
							arr = append(arr, struct {
								id       int
								snapshot clientSessionSnapshot
							}{id: id, snapshot: snapshot})
						}
						continue
					}
					if isClose {
						arr = append(arr, struct {
							id       int
							snapshot clientSessionSnapshot
						}{id: id, snapshot: snapshot})
					}
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

func fileAccessKeyOwnedByClient(clientID int, key string) bool {
	if clientID <= 0 || key == "" || file.GetDb() == nil || file.GetDb().JsonDb == nil {
		return false
	}
	owned := false
	file.GetDb().JsonDb.Tasks.Range(func(_, value interface{}) bool {
		task, ok := value.(*file.Tunnel)
		if !ok || task == nil {
			return true
		}
		task.RLock()
		mode, client, target := task.Mode, task.Client, task.Target
		task.RUnlock()
		if mode != "file" || client == nil || target == nil {
			return true
		}
		client.RLock()
		ownerID := client.Id
		client.RUnlock()
		if ownerID != clientID {
			return true
		}
		target.RLock()
		targetStr := target.TargetStr
		target.RUnlock()
		registeredKey, valid := file.ParseFileAccessTarget(targetStr)
		if valid && registeredKey == key {
			owned = true
			return false
		}
		return true
	})
	return owned
}

func updateFileTaskTarget(clientID, port int, targetValue string) bool {
	updated := false
	file.GetDb().JsonDb.Tasks.Range(func(_, value interface{}) bool {
		task, ok := value.(*file.Tunnel)
		if !ok || task == nil {
			return true
		}
		task.RLock()
		mode, currentPort, client, target := task.Mode, task.Port, task.Client, task.Target
		task.RUnlock()
		if mode != "file" || currentPort != port || client == nil || target == nil {
			return true
		}
		client.RLock()
		ownerID := client.Id
		client.RUnlock()
		if ownerID != clientID {
			return true
		}
		target.Lock()
		target.TargetStr = targetValue
		target.Unlock()
		if err := file.GetDb().UpdateTask(task); err != nil {
			logs.Warn("[文件访问] 保存文件通道键失败：clientId %d port %d: %v", clientID, port, err)
			return false
		}
		updated = true
		return false
	})
	return updated
}

func nestedTunnelOwnerAllowed(sourceClientID, destinationTaskID int) bool {
	if sourceClientID < 0 {
		return true
	}
	sourceClient, err := file.GetDb().GetClient(sourceClientID)
	if err != nil || sourceClient == nil {
		return false
	}
	destination, err := file.GetDb().GetTask(destinationTaskID)
	if err != nil || destination == nil {
		return false
	}
	destination.RLock()
	destinationClient := destination.Client
	destination.RUnlock()
	if destinationClient == nil {
		return false
	}
	sourceClient.RLock()
	sourceUserID, sourceID := sourceClient.UserId, sourceClient.Id
	sourceClient.RUnlock()
	destinationClient.RLock()
	destinationUserID, destinationClientID := destinationClient.UserId, destinationClient.Id
	destinationClient.RUnlock()
	return nestedTunnelTenantAllowed(sourceID, sourceUserID, destinationClientID, destinationUserID)
}

func nestedTunnelTenantAllowed(sourceClientID, sourceUserID, destinationClientID, destinationUserID int) bool {
	if sourceClientID < 0 {
		return true
	}
	if sourceUserID > 0 {
		return destinationUserID == sourceUserID
	}
	// User-owned resources may chain within the same tenant. For unassigned
	// administrator resources, require the same client as well: a user may be
	// granted access to one such client without gaining access to every other
	// unassigned client through tunnel://.
	return sourceClientID == destinationClientID && destinationUserID <= 0
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
			if h.Target != nil && !bridgeAllowsLocalProxy(client.Id) {
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
				if t.Target != nil && !bridgeAllowsLocalProxy(client.Id) {
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
					tl.TargetType = t.TargetType
					tl.LocalPath = t.LocalPath
					tl.StripPre = t.StripPre
					tl.MultiAccount = t.MultiAccount
					if tl.Mode == "file" {
						client.RLock()
						verifyKey := client.VerifyKey
						client.RUnlock()
						key := file.FileAccessKey(verifyKey, tl.LocalPath, tl.StripPre)
						if key == "" {
							fail = true
							c.WriteAddFail()
							break loop
						}
						tl.Target = &file.Target{TargetStr: file.FileAccessTargetPrefix + key}
					}
					tl.Health = file.HealthConfigSnapshot(&t.Health)
					s.configMu.Lock()
					hasTunnel := client.HasTunnel(tl)
					if hasTunnel && tl.Mode == "file" {
						if !updateFileTaskTarget(client.Id, tl.Port, tl.Target.TargetStr) {
							s.configMu.Unlock()
							fail = true
							c.WriteAddFail()
							break loop
						}
					}
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
