package client

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
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
	if m.cfg.SecretLink {
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
	peers := make([]*modernP2PPeer, 0, len(m.peers))
	for _, peer := range m.peers {
		peers = append(peers, peer)
	}
	m.peers = make(map[string]*modernP2PPeer)
	m.mu.Unlock()
	for _, listener := range listeners {
		_ = listener.Close()
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

// StartLocalServer starts one local TCP ingress backed by a P2P task. The
// local listener remains available while direct connectivity is negotiated;
// when configured, Secret relay handles requests until a direct QUIC path is
// ready or whenever that path fails.
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
	fallback := local.Fallback || m.cfg.FallbackSecret || local.Type == "secret"
	if local.Type == "p2p" {
		m.peer(local.Password, "P2P 本地端口 "+strconv.Itoa(local.Port), fallback)
	}
	logs.Info("P2P/Secret 本地监听已启动：%s", listener.Addr())
	go func() {
		for {
			c, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go m.handleLocal(local, fallback, c)
		}
	}()
	return nil
}

func (m *P2PManager) handleLocal(local *config.LocalServer, fallback bool, source net.Conn) {
	defer source.Close()
	compress := m.cfg.Client != nil && m.cfg.Client.Cnf != nil && m.cfg.Client.Cnf.Compress
	link := conn.NewLink(common.CONN_TCP, local.Target, false, compress, source.RemoteAddr().String(), local.LocalProxy, "")
	var remote net.Conn
	var err error
	if local.Type == "secret" {
		// A Secret listener is a relay by definition; it must not negotiate a
		// P2P session for a task that may not even be a P2P task.
		link.Option.NeedAck = m.cfg.SecretLink
		remote, err = m.sendViaSecret(local.Password, *link)
	} else {
		// NeedAck is only meaningful on Secret Link metadata. A direct P2P
		// transport decides independently whether it needs a liveness ACK.
		link.Option.NeedAck = m.cfg.SecretLink
		remote, err = m.SendLinkInfo(local.Password, *link, fallback)
	}
	if err != nil {
		logs.Warn("P2P/Secret 转发连接建立失败：%v", err)
		return
	}
	defer remote.Close()
	conn.CopyWaitGroup(remote, source, false, compress, nil, nil, false, nil, nil, nil)
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
