package client

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/config"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego/logs"
	"github.com/djylb/holepunch"
	"github.com/djylb/holepunch/portmapper"
	"github.com/google/uuid"
)

// DisableP2P is kept as an operator override for compatibility with the
// command-line client. The server still keeps the old UDP/KCP path available
// when this switch is set or the NPC is older than the modern release.
var DisableP2P bool

type serverInfo struct {
	fingerprint []byte
	ip          string
}

// P2PManager owns modern direct paths for one NPC process. It deliberately
// uses the existing authenticated Bridge dialer for control and Secret
// fallback, so the main mux and ordinary proxy streams are never blocked by a
// hole-punch attempt.
type P2PManager struct {
	ctx       context.Context
	cancel    context.CancelFunc
	cfg       *config.CommonConfig
	mu        sync.Mutex
	wg        sync.WaitGroup
	peers     map[string]*modernP2PPeer
	mapper    *portmapper.Mapper
	server    serverInfo
	listeners []net.Listener
	udpConns  []*net.UDPConn
	uuid      string
}

func (m *P2PManager) getUUID() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	if m.uuid == "" {
		m.uuid = uuid.NewString()
	}
	id := m.uuid
	m.mu.Unlock()
	return id
}

func NewP2PManager(parent context.Context, cfg *config.CommonConfig) *P2PManager {
	crypt.EnsureTls()
	ctx, cancel := context.WithCancel(parent)
	m := &P2PManager{
		ctx:    ctx,
		cancel: cancel,
		cfg:    cfg,
		peers:  make(map[string]*modernP2PPeer),
		mapper: portmapper.New(portmapper.Config{Network: holepunch.SystemNetwork()}),
		uuid:   uuid.NewString(),
	}
	return m
}

func (m *P2PManager) setServer(info serverInfo) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.server = info
	m.mu.Unlock()
}

func (m *P2PManager) serverInfo() serverInfo {
	m.mu.Lock()
	info := m.server
	m.mu.Unlock()
	return info
}

func (m *P2PManager) peer(password, label string, relay bool) *modernP2PPeer {
	m.mu.Lock()
	defer m.mu.Unlock()
	peer := m.peers[password]
	if peer == nil {
		peer = newModernP2PPeer(m, password, label)
		m.peers[password] = peer
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			peer.run()
		}()
	}
	if relay {
		peer.relay.Store(true)
	}
	return peer
}

func (m *P2PManager) openTunnelConn() (net.Conn, serverInfo, error) {
	if m == nil || m.cfg == nil {
		return nil, serverInfo{}, errors.New("P2P 配置不可用")
	}
	info := m.serverInfo()
	c, err := NewConnWithTLS(m.cfg.Tp, m.cfg.VKey, m.cfg.Server, common.WORK_P2P,
		m.cfg.ProxyUrl, m.cfg.TlsEnable, TLSOptionsFromConfig(m.cfg))
	if err != nil {
		return nil, info, err
	}
	if len(c.PeerFingerprint) > 0 {
		info.fingerprint = append([]byte(nil), c.PeerFingerprint...)
		m.setServer(info)
	}
	return c.Conn, info, nil
}

func (m *P2PManager) getSecretConn() (net.Conn, error) {
	if m == nil || m.cfg == nil {
		return nil, errors.New("Secret 配置不可用")
	}
	c, err := NewConnWithTLS(m.cfg.Tp, m.cfg.VKey, m.cfg.Server, common.WORK_SECRET,
		m.cfg.ProxyUrl, m.cfg.TlsEnable, TLSOptionsFromConfig(m.cfg))
	if err != nil {
		return nil, err
	}
	return c.Conn, nil
}

func (m *P2PManager) SendLinkInfo(password string, link conn.Link, fallback bool) (net.Conn, error) {
	if m == nil || link.Host == "" {
		return nil, errors.New("P2P 参数不完整")
	}
	peer := m.peer(password, "P2P "+password, fallback)
	if !DisableP2P {
		if transport := peer.active(); transport != nil {
			if c, err := transport.openLink(m.ctx, link); err == nil {
				return c, nil
			} else {
				peer.suspect(transport, err)
				if !fallback {
					return nil, err
				}
				logs.Info("P2P 直连失败，继续使用 Secret 中继：%v", err)
			}
		}
		if !fallback {
			ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
			transport := peer.waitActive(ctx)
			cancel()
			if transport != nil {
				if c, err := transport.openLink(m.ctx, link); err == nil {
					return c, nil
				} else {
					peer.suspect(transport, err)
					return nil, err
				}
			}
			return nil, errors.New("P2P 直连尚未建立，且未启用 Secret 回退")
		}
	}
	if !fallback {
		return nil, errors.New("P2P 直连不可用，且未启用 Secret 回退")
	}
	return m.sendViaSecret(password, link)
}

func (m *P2PManager) sendViaSecret(password string, link conn.Link) (net.Conn, error) {
	sc, err := m.getSecretConn()
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = sc.Close()
		}
	}()
	if _, err = sc.Write([]byte(crypt.Md5(password))); err != nil {
		return nil, err
	}
	// UDP datagrams always need the Link record so the server can select the
	// datagram path; TCP links keep the historical gate so legacy servers
	// receive raw bytes exactly as before.
	if m.cfg.SecretLink || link.ConnType == common.CONN_UDP {
		if _, err = conn.NewConn(sc).SendInfo(&link, ""); err != nil {
			return nil, err
		}
		if link.Option.NeedAck {
			if err = conn.ReadACK(sc, link.Option.Timeout); err != nil {
				return nil, err
			}
		}
	}
	ok = true
	return sc, nil
}

func (m *P2PManager) Close() {
	if m == nil {
		return
	}
	m.cancel()
	m.mu.Lock()
	listeners := append([]net.Listener(nil), m.listeners...)
	m.listeners = nil
	udpConns := append([]*net.UDPConn(nil), m.udpConns...)
	m.udpConns = nil
	peers := make([]*modernP2PPeer, 0, len(m.peers))
	for _, peer := range m.peers {
		peers = append(peers, peer)
	}
	m.peers = make(map[string]*modernP2PPeer)
	m.mu.Unlock()
	for _, listener := range listeners {
		_ = listener.Close()
	}
	for _, conn := range udpConns {
		_ = conn.Close()
	}
	for _, peer := range peers {
		if transport := peer.active(); transport != nil {
			transport.Close(0, "client closing")
		}
	}
	if m.mapper != nil {
		_ = m.mapper.Close()
	}
	m.wg.Wait()
}

func modernSleepWithContext(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// StartLocalServer starts the local ingress listeners backed by a P2P task.
// The target type selects TCP, UDP, or both listeners on the same local port.
// Listeners remain available while direct connectivity is negotiated; when
// configured, Secret relay handles requests until a direct QUIC path is ready
// or whenever that path fails.
func (m *P2PManager) StartLocalServer(local *config.LocalServer) error {
	if m == nil || local == nil || m.ctx.Err() != nil {
		return errors.New("P2P 本地服务配置无效或已关闭")
	}
	if local.Type != "p2p" && local.Type != "secret" {
		return errors.New("现代本地转发仅支持 p2p 和 secret 类型")
	}
	crypt.EnsureTls()
	port := local.Port
	ip := strings.TrimSpace(local.Ip)
	if ip == "" {
		ip = "0.0.0.0"
	}
	fallback := local.Fallback || m.cfg.FallbackSecret || local.Type == "secret"
	compress := m.cfg.Client != nil && m.cfg.Client.Cnf != nil && m.cfg.Client.Cnf.Compress
	if local.Type == "p2p" {
		m.peer(local.Password, "P2P 本地端口 "+strconv.Itoa(local.Port), fallback)
	}
	targetType := normalizeP2PTargetType(local.TargetType)
	var errs []error
	if targetType == common.CONN_TCP || targetType == common.CONN_ALL {
		if err := m.startLocalTCP(local, ip, port, fallback, compress); err != nil {
			errs = append(errs, err)
		}
	}
	if targetType == common.CONN_UDP || targetType == common.CONN_ALL {
		if err := m.startLocalUDP(local, ip, port, fallback, compress); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *P2PManager) startLocalTCP(local *config.LocalServer, ip string, port int, fallback, compress bool) error {
	listener, err := net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	m.mu.Lock()
	if m.ctx.Err() != nil {
		m.mu.Unlock()
		_ = listener.Close()
		return errors.New("P2P 服务在监听启动前已关闭")
	}
	m.listeners = append(m.listeners, listener)
	m.mu.Unlock()
	logs.Info("P2P/Secret 本地 TCP 监听已启动：%s", listener.Addr())
	go func() {
		for {
			c, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go m.handleLocal(local, fallback, compress, c)
		}
	}()
	return nil
}

// normalizeP2PTargetType maps the stored target type to one of tcp, udp, or
// all. Empty and unknown values keep the historical TCP-only behavior.
func normalizeP2PTargetType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case common.CONN_UDP, common.CONN_ALL:
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return common.CONN_TCP
	}
}

// openLocalLink builds and opens one visitor-to-provider stream for a local
// listener. UDP links carry the framing flag so the provider preserves
// datagram boundaries.
func (m *P2PManager) openLocalLink(local *config.LocalServer, fallback bool, connType, remoteAddr string, compress bool) (net.Conn, error) {
	if m == nil || local == nil || strings.TrimSpace(local.Target) == "" {
		return nil, errors.New("P2P 本地目标为空")
	}
	link := conn.NewLink(common.CONN_TCP, local.Target, false, compress, remoteAddr, local.LocalProxy, "")
	link.ConnType = connType
	if connType == common.CONN_UDP {
		link.DatagramFramed = true
	}
	link.Option.NeedAck = m.cfg.SecretLink
	if local.Type == "secret" {
		// A Secret listener is a relay by definition; it must not negotiate a
		// P2P session for a task that may not even be a P2P task.
		return m.sendViaSecret(local.Password, *link)
	}
	// NeedAck is only meaningful on Secret Link metadata. A direct P2P
	// transport decides independently whether it needs a liveness ACK.
	return m.SendLinkInfo(local.Password, *link, fallback)
}

func (m *P2PManager) handleLocal(local *config.LocalServer, fallback, compress bool, source net.Conn) {
	defer source.Close()
	remote, err := m.openLocalLink(local, fallback, common.CONN_TCP, source.RemoteAddr().String(), compress)
	if err != nil {
		logs.Warn("P2P/Secret 转发连接建立失败：%v", err)
		return
	}
	defer remote.Close()
	conn.CopyWaitGroup(remote, source, false, compress, nil, nil, false, nil, nil, nil)
}

const (
	p2pUDPIdleTimeout    = 120 * time.Second
	p2pUDPSweepInterval  = 30 * time.Second
	p2pUDPBuildTimeout   = 10 * time.Second
	p2pUDPDatagramBuffer = 64 << 10
)

// p2pUDPListener is one local UDP ingress of a P2P/Secret visitor. It keeps
// one framed provider stream per visitor source address, mirroring the
// server-side UDP mode server but routing the links through the P2P manager.
type p2pUDPListener struct {
	mgr      *P2PManager
	local    *config.LocalServer
	fallback bool
	compress bool
	conn     *net.UDPConn
	sessions sync.Map
	done     chan struct{}
}

type p2pUDPSession struct {
	mu       sync.Mutex
	ready    chan struct{}
	buildErr error
	framed   *conn.FramedConn
	raw      net.Conn
	closed   bool
	last     atomic.Int64
}

func (s *p2pUDPSession) touch() {
	s.last.Store(time.Now().UnixNano())
}

func (s *p2pUDPSession) waitReady() error {
	if s.ready == nil {
		return nil
	}
	select {
	case <-s.ready:
		s.mu.Lock()
		err := s.buildErr
		s.mu.Unlock()
		return err
	case <-time.After(p2pUDPBuildTimeout):
		return errors.New("P2P UDP 会话建立超时")
	}
}

func (s *p2pUDPSession) write(data []byte) error {
	if err := s.waitReady(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed || s.framed == nil {
		s.mu.Unlock()
		return io.ErrClosedPipe
	}
	_, err := s.framed.Write(data)
	s.mu.Unlock()
	return err
}

func (s *p2pUDPSession) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	raw := s.raw
	s.mu.Unlock()
	if raw != nil {
		_ = raw.Close()
	}
}

func (m *P2PManager) startLocalUDP(local *config.LocalServer, ip string, port int, fallback, compress bool) error {
	udpAddr := &net.UDPAddr{IP: net.ParseIP(ip), Port: port}
	pc, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return err
	}
	m.mu.Lock()
	if m.ctx.Err() != nil {
		m.mu.Unlock()
		_ = pc.Close()
		return errors.New("P2P 服务在监听启动前已关闭")
	}
	m.udpConns = append(m.udpConns, pc)
	m.mu.Unlock()
	listener := &p2pUDPListener{
		mgr:      m,
		local:    local,
		fallback: fallback,
		compress: compress,
		conn:     pc,
		done:     make(chan struct{}),
	}
	logs.Info("P2P/Secret 本地 UDP 监听已启动：%s", pc.LocalAddr())
	go listener.run()
	go listener.sweep()
	return nil
}

func (l *p2pUDPListener) run() {
	defer func() {
		close(l.done)
		l.sessions.Range(func(key, value any) bool {
			l.remove(key.(string), value.(*p2pUDPSession))
			return true
		})
	}()
	buf := make([]byte, p2pUDPDatagramBuffer)
	for {
		n, addr, err := l.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		data := make([]byte, n)
		copy(data, buf[:n])
		l.dispatch(addr, data)
	}
}

func (l *p2pUDPListener) dispatch(addr *net.UDPAddr, data []byte) {
	key := addr.String()
	if existing, ok := l.sessions.Load(key); ok {
		sess := existing.(*p2pUDPSession)
		if err := sess.write(data); err != nil {
			l.remove(key, sess)
		} else {
			sess.touch()
		}
		return
	}
	sess := &p2pUDPSession{ready: make(chan struct{})}
	sess.touch()
	if existing, loaded := l.sessions.LoadOrStore(key, sess); loaded {
		winner := existing.(*p2pUDPSession)
		if err := winner.write(data); err != nil {
			l.remove(key, winner)
		} else {
			winner.touch()
		}
		return
	}
	go l.build(addr, key, sess, data)
}

func (l *p2pUDPListener) build(addr *net.UDPAddr, key string, sess *p2pUDPSession, first []byte) {
	fail := func(err error) {
		sess.mu.Lock()
		sess.buildErr = err
		sess.mu.Unlock()
		close(sess.ready)
		l.remove(key, sess)
	}
	remote, err := l.mgr.openLocalLink(l.local, l.fallback, common.CONN_UDP, addr.String(), l.compress)
	if err != nil {
		fail(err)
		return
	}
	rwc := conn.GetConnWithLimits(remote, false, l.compress, nil, false, nil, nil)
	sess.mu.Lock()
	if sess.closed {
		sess.mu.Unlock()
		_ = remote.Close()
		fail(errors.New("P2P UDP 会话在建立前已关闭"))
		return
	}
	sess.framed = conn.NewFramedConn(rwc)
	sess.raw = remote
	sess.mu.Unlock()
	close(sess.ready)
	sess.touch()
	go l.readLoop(addr, key, sess)
	if err := sess.write(first); err != nil {
		l.remove(key, sess)
	}
}

func (l *p2pUDPListener) readLoop(addr *net.UDPAddr, key string, sess *p2pUDPSession) {
	defer l.remove(key, sess)
	buf := make([]byte, p2pUDPDatagramBuffer)
	for {
		sess.mu.Lock()
		framed := sess.framed
		sess.mu.Unlock()
		if framed == nil {
			return
		}
		n, err := framed.Read(buf)
		if n > 0 {
			if _, werr := l.conn.WriteToUDP(buf[:n], addr); werr != nil {
				return
			}
			sess.touch()
		}
		if err != nil {
			return
		}
	}
}

func (l *p2pUDPListener) remove(key string, sess *p2pUDPSession) {
	l.sessions.CompareAndDelete(key, sess)
	sess.close()
}

func (l *p2pUDPListener) sweep() {
	ticker := time.NewTicker(p2pUDPSweepInterval)
	defer ticker.Stop()
	idleNs := int64(p2pUDPIdleTimeout)
	for {
		select {
		case <-l.done:
			return
		case <-ticker.C:
			now := time.Now().UnixNano()
			l.sessions.Range(func(key, value any) bool {
				sess := value.(*p2pUDPSession)
				if now-sess.last.Load() > idleNs {
					l.remove(key.(string), sess)
				}
				return true
			})
		}
	}
}

// p2pBridgeAdapter is used by the existing local listener code. It keeps the
// local listener API unchanged while routing its links through the modern
// manager when one is active.
type p2pBridgeAdapter struct {
	manager  *P2PManager
	password string
	fallback bool
}

func (b *p2pBridgeAdapter) SendLinkInfo(_ int, link *conn.Link, _ *file.Tunnel) (net.Conn, error) {
	if b == nil || b.manager == nil || link == nil {
		return nil, errors.New("P2P manager unavailable")
	}
	return b.manager.SendLinkInfo(b.password, *link, b.fallback)
}

func (b *p2pBridgeAdapter) IsServer() bool { return false }

func (b *p2pBridgeAdapter) CliProcess(*conn.Conn, string) {}
