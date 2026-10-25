package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/nps_mux"
	"github.com/pires/go-proxyproto"

	"github.com/astaxie/beego/logs"
	"github.com/xtaci/kcp-go"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/config"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/p2pproto"
)

// ClientLogger receives logs emitted by one TRP client instance. It lets
// embedders route a client's logs independently without replacing the
// process-wide Beego logger.
type ClientLogger interface {
	Info(format string, v ...interface{})
	Error(format string, v ...interface{})
	Warn(format string, v ...interface{})
	Trace(format string, v ...interface{})
}

var remoteUpdateHandler struct {
	sync.RWMutex
	fn func(vKey string) (bool, error)
}

var remoteUpdateInProgress atomic.Bool

// SetRemoteUpdateHandler registers the executable-level updater. The client
// package handles the wire signal, while the NPC command chooses the
// platform-appropriate process/service restart strategy.
func SetRemoteUpdateHandler(handler func(vKey string) (bool, error)) {
	remoteUpdateHandler.Lock()
	remoteUpdateHandler.fn = handler
	remoteUpdateHandler.Unlock()
	remoteUpdateInProgress.Store(false)
}

func requestRemoteUpdate(client *TRPClient) {
	if client == nil {
		return
	}
	// The server may expose a global update button, but the local operator can
	// explicitly disable remote updates in npc.conf.  Check this before
	// acquiring the process-wide update lock so an ignored request never blocks
	// a later, manually enabled client instance.
	if client.cnf != nil && client.cnf.CommonConfig != nil && client.cnf.CommonConfig.DisableRemoteUpdate {
		client.logInfo("[升级] 配置已禁止服务端下发的远程升级请求")
		return
	}
	if !remoteUpdateInProgress.CompareAndSwap(false, true) {
		client.logInfo("remote client update is already in progress")
		return
	}
	go func() {
		remoteUpdateHandler.RLock()
		handler := remoteUpdateHandler.fn
		remoteUpdateHandler.RUnlock()
		if handler == nil {
			client.logError("[更新异常] 服务端请求客户端升级，但当前进程没有注册升级器")
			remoteUpdateInProgress.Store(false)
			return
		}
		restartRequested, err := handler(client.vKey)
		if err != nil {
			client.logError("[更新异常] 服务端下发的客户端升级失败：%s", err.Error())
			remoteUpdateInProgress.Store(false)
			return
		}
		if !restartRequested {
			remoteUpdateInProgress.Store(false)
		}
	}()
}

type TRPClient struct {
	svrAddr        string
	bridgeConnType string
	proxyUrl       string
	localIP        string
	localIPForward bool
	tlsEnable      bool
	tlsOptions     TLSOptions
	vKey           string
	p2pAddr        map[string]string
	tunnel         *nps_mux.Mux
	signal         *conn.Conn
	cnf            *config.Config
	disconnectTime int
	once           sync.Once
	stateMu        sync.RWMutex
	loggerMu       sync.RWMutex
	closed         atomic.Bool
	closeCh        chan struct{} // closed when client is shutting down; stops ping
	logger         ClientLogger  // 每个客户端独立的 logger
	healthMu       sync.Mutex
	healthStop     chan struct{}
	remoteHealths  []*file.Health
	p2pManager     *P2PManager
	serverInfo     serverInfo
}

// new client
func NewRPClient(svraddr string, vKey string, bridgeConnType string, proxyUrl string, cnf *config.Config, disconnectTime int) *TRPClient {
	return NewRPClientWithTLS(svraddr, vKey, bridgeConnType, proxyUrl, cnf, disconnectTime, GetTlsEnable())
}

// NewRPClientWithTLS creates a client with an instance-scoped TLS preference.
// NewRPClient remains as a compatibility wrapper for callers that use the
// legacy process-wide SetTlsEnable API.
func NewRPClientWithTLS(svraddr string, vKey string, bridgeConnType string, proxyUrl string, cnf *config.Config, disconnectTime int, tlsEnable bool, tlsOptions ...TLSOptions) *TRPClient {
	options := TLSOptions{}
	if len(tlsOptions) > 0 {
		options = tlsOptions[0]
	}
	var p2pManager *P2PManager
	if cnf != nil && cnf.CommonConfig != nil {
		p2pManager = NewP2PManager(context.Background(), cnf.CommonConfig)
	}
	return &TRPClient{
		svrAddr:        svraddr,
		tlsEnable:      tlsEnable,
		tlsOptions:     options,
		p2pAddr:        make(map[string]string, 0),
		vKey:           vKey,
		bridgeConnType: bridgeConnType,
		proxyUrl:       proxyUrl,
		localIP:        options.LocalIP,
		localIPForward: options.LocalIPForward,
		cnf:            cnf,
		disconnectTime: disconnectTime,
		once:           sync.Once{},
		closeCh:        make(chan struct{}),
		logger:         nil, // 默认使用全局 logger，可通过 SetLogger 设置
		p2pManager:     p2pManager,
	}
}

// SetLogger 设置客户端的独立 logger。
func (s *TRPClient) SetLogger(logger ClientLogger) {
	s.loggerMu.Lock()
	s.logger = logger
	s.loggerMu.Unlock()
}

// SetP2PManager injects the local forwarding manager created from the same
// NPC configuration. It keeps local listeners and the control channel on one
// lifecycle while preserving constructor compatibility for SDK callers.
func (s *TRPClient) SetP2PManager(manager *P2PManager) {
	if s == nil || manager == nil {
		return
	}
	if s.p2pManager != nil && s.p2pManager != manager {
		s.p2pManager.Close()
	}
	s.p2pManager = manager
}

func (s *TRPClient) instanceLogger() ClientLogger {
	s.loggerMu.RLock()
	logger := s.logger
	s.loggerMu.RUnlock()
	return logger
}

// log 辅助方法：如果设置了独立 logger 就使用，否则使用全局 logger
func (s *TRPClient) logInfo(format string, v ...interface{}) {
	if logger := s.instanceLogger(); logger != nil {
		logger.Info(format, v...)
	} else {
		logs.Info(format, v...)
	}
}

func (s *TRPClient) logError(format string, v ...interface{}) {
	if logger := s.instanceLogger(); logger != nil {
		logger.Error(format, v...)
	} else {
		logs.Error(format, v...)
	}
}

func (s *TRPClient) logWarn(format string, v ...interface{}) {
	if logger := s.instanceLogger(); logger != nil {
		logger.Warn(format, v...)
	} else {
		logs.Warn(format, v...)
	}
}

func (s *TRPClient) logTrace(format string, v ...interface{}) {
	if logger := s.instanceLogger(); logger != nil {
		logger.Trace(format, v...)
	} else {
		logs.Trace(format, v...)
	}
}

// IsConnected 返回客户端是否已成功连接到服务器
func (s *TRPClient) IsConnected() bool {
	s.stateMu.RLock()
	connected := s.signal != nil && !s.closed.Load()
	s.stateMu.RUnlock()
	return connected
}

func (s *TRPClient) currentSignal() *conn.Conn {
	s.stateMu.RLock()
	signal := s.signal
	s.stateMu.RUnlock()
	return signal
}

func (s *TRPClient) currentTunnel() *nps_mux.Mux {
	s.stateMu.RLock()
	tunnel := s.tunnel
	s.stateMu.RUnlock()
	return tunnel
}

func (s *TRPClient) installSignal(signal *conn.Conn) bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.closed.Load() {
		return false
	}
	s.signal = signal
	return true
}

func (s *TRPClient) installTunnel(tunnel *nps_mux.Mux) (old *nps_mux.Mux, installed bool) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.closed.Load() {
		return nil, false
	}
	old = s.tunnel
	s.tunnel = tunnel
	return old, true
}

func (s *TRPClient) detach(expectedSignal *conn.Conn, expectedTunnel *nps_mux.Mux) (signal *conn.Conn, tunnel *nps_mux.Mux, detached bool) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if expectedSignal != nil && s.signal != expectedSignal {
		return nil, nil, false
	}
	if expectedTunnel != nil && s.tunnel != expectedTunnel {
		return nil, nil, false
	}
	signal, tunnel = s.signal, s.tunnel
	s.signal, s.tunnel = nil, nil
	return signal, tunnel, true
}

func (s *TRPClient) closeDetached(signal *conn.Conn, tunnel *nps_mux.Mux) {
	if s.p2pManager != nil {
		s.p2pManager.Close()
	}
	s.stopHealthChecks()
	s.once.Do(func() {
		s.closed.Store(true)
		s.stateMu.Lock()
		s.serverInfo = serverInfo{}
		s.stateMu.Unlock()
		CloseClient.Store(true)
		NowStatus.Store(0)
		if s.closeCh == nil {
			s.closeCh = make(chan struct{})
		}
		select {
		case <-s.closeCh:
		default:
			close(s.closeCh)
		}
	})
	// Resource closes are idempotent and intentionally happen outside the
	// once callback. This covers a race where a stale reader detaches its
	// resources just after another caller wins the once guard.
	if tunnel != nil {
		_ = tunnel.Close()
	}
	if signal != nil {
		_ = signal.Close()
	}
}

var NowStatus atomic.Int32
var CloseClient atomic.Bool

// start
func (s *TRPClient) Start() {
	if s.closed.Load() {
		return
	}
retry:
	// The lifecycle belongs to this client instance. CloseClient is kept as a
	// legacy SDK status flag, but must not let one GUI client stop another.
	if s.closed.Load() {
		return
	}
	NowStatus.Store(0)
	c, err := NewConnWithTLS(s.bridgeConnType, s.vKey, s.svrAddr, common.WORK_MAIN, s.proxyUrl, s.tlsEnable, s.tlsOptions)
	if err != nil {
		s.logError("[连接异常] 连接服务端失败，5 秒后重连：%s", err.Error())
		if !s.waitReconnect() {
			return
		}
		goto retry
	}
	if c == nil {
		s.logError("[连接异常] 服务端返回了空连接，5 秒后重连")
		if !s.waitReconnect() {
			return
		}
		goto retry
	}
	if s.closed.Load() {
		_ = c.Close()
		return
	}
	s.logInfo("[连接] 已连接服务端 %s", s.svrAddr)
	if s.p2pManager != nil {
		info := serverInfo{fingerprint: append([]byte(nil), c.PeerFingerprint...), ip: common.GetIpByAddr(c.Conn.RemoteAddr().String())}
		s.p2pManager.setServer(info)
		s.stateMu.Lock()
		s.serverInfo = info
		s.stateMu.Unlock()
	}
	//monitor the connection
	go s.ping()
	if !s.installSignal(c) {
		_ = c.Close()
		return
	}
	//start a channel connection
	go s.newChan()
	// Start local checks immediately. The server may subsequently replace the
	// remote portion through HEALTH_CONFIG; that update restarts this session
	// without touching the tunnel/data channels.
	s.startHealthChecks(c)
	NowStatus.Store(1)
	//msg connection, eg udp
	s.handleMain()
}

// waitReconnect makes shutdown responsive while preserving the reconnect
// backoff used by the command-line client.
func (s *TRPClient) waitReconnect() bool {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-s.closeCh:
		return false
	}
}

// handle main connection
func (s *TRPClient) handleMain() {
	signal := s.currentSignal()
	if signal == nil {
		return
	}
mainLoop:
	for {
		flags, err := signal.ReadFlag()
		if err != nil {
			s.logError("[连接异常] 读取服务端控制数据失败，连接结束：%s", err.Error())
			break
		}
		switch flags {
		case common.CLIENT_UPDATE:
			// Update asynchronously so a slow GitHub download never blocks the
			// main control reader or tunnel traffic. The process is restarted only
			// after the new binary has been downloaded and verified.
			requestRemoteUpdate(s)
		case common.P2P_PUNCH_START:
			raw, readErr := signal.GetShortLenContent()
			if readErr != nil {
				s.logWarn("读取 P2P 邀请失败：%s", readErr.Error())
				break mainLoop
			}
			var invite p2pproto.Invite
			if readErr = json.Unmarshal(raw, &invite); readErr != nil {
				s.logWarn("解析 P2P 邀请失败：%s", readErr.Error())
				continue
			}
			s.stateMu.RLock()
			serverFingerprintReady := len(s.serverInfo.fingerprint) > 0
			s.stateMu.RUnlock()
			if s.p2pManager != nil && serverFingerprintReady {
				go s.serveModernP2P(s.currentTunnel(), invite)
			}
		case common.HEALTH_CONFIG:
			length, readErr := signal.GetLen()
			if readErr != nil {
				s.logWarn("读取服务端健康配置长度失败：%s", readErr.Error())
				break mainLoop
			}
			if length < 0 || length > 1<<20 {
				s.logWarn("服务端健康配置长度无效：%d", length)
				break mainLoop
			}
			payload := make([]byte, length)
			_, readErr = io.ReadFull(signal, payload)
			if readErr != nil {
				s.logWarn("读取服务端健康配置失败：%s", readErr.Error())
				break mainLoop
			}
			var profiles []file.Health
			if readErr = json.Unmarshal(payload, &profiles); readErr != nil {
				s.logWarn("解析服务端健康配置失败：%s", readErr.Error())
				break mainLoop
			}
			s.applyRemoteHealthConfig(profiles)
			s.logTrace("[配置] 已同步 %d 个远程健康检查配置", len(profiles))
		case common.REPORT_LOCAL_IP:
			// Newer servers request the client's private/LAN addresses. Older
			// servers never send this flag, so this is protocol-compatible.
			localIPs := common.GetLocalIPs(signal.Conn)
			if err := signal.WriteLenContent([]byte(localIPs)); err != nil {
				s.logWarn("report local address failed: %s", err.Error())
			} else {
				s.logTrace("reported local addr: %s", localIPs)
			}
		case common.NEW_UDP_CONN:
			//read server udp addr and password
			if lAddr, err := signal.GetShortLenContent(); err != nil {
				s.logWarn("读取 P2P 中继地址失败：%s", err)
				break mainLoop
			} else if pwd, err := signal.GetShortLenContent(); err == nil {
				var localAddr string
				//The local port remains unchanged for a certain period of time
				if v, ok := s.p2pAddr[crypt.Md5(string(pwd)+strconv.Itoa(int(time.Now().Unix()/100)))]; !ok {
					tmpConn, err := common.GetLocalUdpAddr()
					if err != nil {
						s.logError("创建 P2P 本地 UDP 地址失败：%s", err)
						break mainLoop
					}
					localAddr = tmpConn.LocalAddr().String()
				} else {
					localAddr = v
				}
				go s.newUdpConn(localAddr, string(lAddr), string(pwd))
			}
		}
	}
	// Only the connection that this loop owns may tear down the client. A
	// reconnect can install a newer signal while the old reader is unwinding.
	if detachedSignal, detachedTunnel, ok := s.detach(signal, nil); ok {
		s.closeDetached(detachedSignal, detachedTunnel)
	}
}

func (s *TRPClient) newUdpConn(localAddr, rAddr string, md5Password string) {
	var localConn net.PacketConn
	var err error
	var remoteAddress string
	if remoteAddress, localConn, err = handleP2PUdp(localAddr, rAddr, md5Password, common.WORK_P2P_PROVIDER); err != nil {
		s.logError("建立 P2P UDP 会话失败：%s", err)
		return
	}
	l, err := kcp.ServeConn(nil, 150, 3, localConn)
	if err != nil {
		_ = localConn.Close()
		s.logError("启动 P2P KCP 会话失败：%s", err)
		return
	}
	defer l.Close()
	s.logTrace("start local p2p udp listen, local address %s", localConn.LocalAddr().String())
	for {
		udpTunnel, err := l.AcceptKCP()
		if err != nil {
			s.logError("接收 P2P KCP 连接失败：%s", err)
			return
		}
		if !matchesP2PRemote(udpTunnel, remoteAddress) {
			continue
		}
		conn.SetUdpSession(udpTunnel)
		s.logTrace("successful connection with client ,address %s", udpTunnel.RemoteAddr().String())
		//read link info from remote
		conn.Accept(nps_mux.NewMux(udpTunnel, muxTransportType(s.bridgeConnType), s.disconnectTime), func(c net.Conn) {
			go s.handleChan(c)
		})
		return
	}
}

// matchesP2PRemote keeps the temporary KCP listener from accumulating
// unauthenticated sessions while it waits for the peer selected by the P2P
// handshake. The listener owns the underlying packet socket; an unmatched
// session must be closed individually rather than left in its session map.
func matchesP2PRemote(session net.Conn, expectedRemote string) bool {
	if session == nil {
		return false
	}
	remote := session.RemoteAddr()
	if remote == nil || remote.String() != expectedRemote {
		_ = session.Close()
		return false
	}
	return true
}

// pmux tunnel
func (s *TRPClient) newChan() {
	tunnel, err := NewConnWithTLS(s.bridgeConnType, s.vKey, s.svrAddr, common.WORK_CHAN, s.proxyUrl, s.tlsEnable, s.tlsOptions)
	if err != nil {
		s.logError("[通道异常] 建立数据通道失败：%s：%v", s.svrAddr, err)
		s.Close()
		return
	}
	newMux := nps_mux.NewMux(tunnel.Conn, muxTransportType(s.bridgeConnType), s.disconnectTime)
	// Install the new mux atomically, then close the previous one outside the
	// state lock. If the client was closed while dialing, discard the new mux.
	oldTunnel, installed := s.installTunnel(newMux)
	if !installed {
		_ = newMux.Close()
		return
	}
	if oldTunnel != nil {
		_ = oldTunnel.Close()
	}
	for {
		src, err := newMux.Accept()
		if err != nil {
			s.logWarn("[通道异常] MUX 接收连接失败：%s", err.Error())
			if detachedSignal, detachedTunnel, ok := s.detach(nil, newMux); ok {
				s.closeDetached(detachedSignal, detachedTunnel)
			}
			break
		}
		go s.handleChan(src)
	}
}

func (s *TRPClient) handleChan(src net.Conn) {
	lk, err := conn.NewConn(src).GetLinkInfo()
	if err != nil || lk == nil {
		src.Close()
		s.logError("[通道异常] 读取转发连接信息失败：%v", err)
		return
	}
	// Modern P2P and Secret Link use a small, optional ACK before the
	// provider starts dialing the target.  It is deliberately handled before
	// the empty-host liveness probe so the probe can prove the path is alive
	// without attempting a real backend connection.  Older links keep
	// NeedAck=false and therefore retain the historical wire flow.
	if lk.Option.NeedAck {
		if err := conn.WriteACK(src, lk.Option.Timeout); err != nil {
			s.logWarn("[通道异常] 写入 ACK 失败：%v", err)
			_ = src.Close()
			return
		}
	}
	//host for target processing
	lk.Host = common.FormatAddress(lk.Host)
	if lk.Host == "" {
		s.logTrace("[P2P] 收到连接探测，来源：%s", lk.RemoteAddr)
		_ = src.Close()
		return
	}
	//if Conn type is http, read the request and log
	if lk.ConnType == "http" {
		if targetConn, err := s.dialTarget(common.CONN_TCP, lk.Host, lk.Option.Timeout); err != nil {
			s.logWarn("connect to %s error %s", lk.Host, err.Error())
			src.Close()
		} else {
			if lk.ProtoVersion == "V1" || lk.ProtoVersion == "V2" {
				if err := writeProxyProtocolHeader(targetConn, lk.RemoteAddr, lk.ProtoVersion); err != nil {
					s.logError("write PROXY protocol header for HTTP backend %s: %v", lk.Host, err)
					_ = targetConn.Close()
					_ = src.Close()
					return
				}
			}
			srcConn := conn.GetConn(src, lk.Crypt, lk.Compress, nil, false, lk.TLSFingerprint)
			var targetCloseOnce sync.Once
			targetWriteClosed := false
			forwardedRequest := false
			closeTarget := func() {
				targetCloseOnce.Do(func() {
					_ = targetConn.Close()
				})
			}
			go func() {
				// Keep the response path full-duplex: this goroutine is the sole
				// reader of targetConn while the request loop reads srcConn.
				common.CopyBuffer(srcConn, targetConn)
				// It is also the sole owner of srcConn's final close, so a
				// compressed writer cannot be closed while CopyBuffer is writing.
				_ = srcConn.Close()
				closeTarget()
			}()
			// Keep one reader for the full stream: replacing it per request can
			// lose bytes already prefetched from a pipelined client connection.
			reader := bufio.NewReader(srcConn)
			for {
				r, err := http.ReadRequest(reader)
				if err != nil {
					if errors.Is(err, io.EOF) && forwardedRequest {
						if halfCloser, ok := targetConn.(interface{ CloseWrite() error }); ok {
							targetWriteClosed = halfCloser.CloseWrite() == nil
						}
					}
					if !targetWriteClosed {
						closeTarget()
					}
					break
				}
				remoteAddr := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
				if len(remoteAddr) == 0 {
					remoteAddr = r.RemoteAddr
				}
				s.logTrace("http request, method %s, host %s, url %s, remote address %s", r.Method, r.Host, r.URL.Path, remoteAddr)
				if err := r.Write(targetConn); err != nil {
					s.logTrace("forward http request error: %v", err)
					closeTarget()
					break
				}
				forwardedRequest = true
			}
			if !targetWriteClosed {
				closeTarget()
			}
		}
		return
	}
	if lk.ConnType == "udp5" {
		s.logTrace("new %s connection with the goal of %s, remote address:%s", lk.ConnType, lk.Host, lk.RemoteAddr)
		localIP := ""
		if s.localIPForward {
			localIP = s.localIP
		}
		s.handleUdp(conn.GetConn(src, lk.Crypt, lk.Compress, nil, false, lk.TLSFingerprint), localIP)
		return
	}
	//connect to target if conn type is tcp or udp
	targetConn, dialErr := s.dialTarget(lk.ConnType, lk.Host, lk.Option.Timeout)
	if dialErr != nil {
		s.logWarn("connect to %s error %s", lk.Host, dialErr.Error())
		src.Close()
	} else {
		s.logTrace("new %s connection with the goal of %s, remote address:%s", lk.ConnType, lk.Host, lk.RemoteAddr)

		if lk.ProtoVersion == "V1" || lk.ProtoVersion == "V2" {
			if err := writeProxyProtocolHeader(targetConn, lk.RemoteAddr, lk.ProtoVersion); err != nil {
				s.logError("write PROXY protocol header for backend %s: %v", lk.Host, err)
				_ = targetConn.Close()
				_ = src.Close()
				return
			}
		}

		conn.CopyWaitGroup(src, targetConn, lk.Crypt, lk.Compress, nil, nil, false, nil, nil, nil, lk.TLSFingerprint)
	}
}

func (s *TRPClient) forwardLocalIP(host string) string {
	if s != nil && s.localIPForward && s.localIP != "" && common.IsPublicHost(host) {
		return s.localIP
	}
	return ""
}

func (s *TRPClient) dialTarget(network, host string, timeout time.Duration) (net.Conn, error) {
	if localIP := s.forwardLocalIP(host); localIP != "" {
		dialer := net.Dialer{Timeout: timeout}
		if network == "udp" {
			dialer.LocalAddr = common.BuildUDPBindAddr(localIP)
		} else {
			dialer.LocalAddr = common.BuildTCPBindAddr(localIP)
		}
		return dialer.DialContext(context.Background(), network, host)
	}
	return net.DialTimeout(network, host, timeout)
}

func writeProxyProtocolHeader(target net.Conn, remoteAddr, protoVersion string) error {
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
		return errors.New("unsupported PROXY protocol version " + protoVersion)
	}
	sourceAddr, err := net.ResolveTCPAddr("tcp", strings.TrimSpace(remoteAddr))
	if err != nil || sourceAddr == nil || sourceAddr.IP == nil {
		return errors.New("invalid source address for PROXY protocol: " + remoteAddr)
	}
	destinationAddr, ok := target.RemoteAddr().(*net.TCPAddr)
	if !ok || destinationAddr == nil || destinationAddr.IP == nil {
		return errors.New("invalid destination address for PROXY protocol")
	}
	sourceIsIPv4 := sourceAddr.IP.To4() != nil
	destinationIsIPv4 := destinationAddr.IP.To4() != nil
	if sourceIsIPv4 != destinationIsIPv4 {
		return errors.New("PROXY protocol cannot represent mixed address families")
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
	if _, err := header.WriteTo(target); err != nil {
		return err
	}
	return nil
}

func (s *TRPClient) handleUdp(serverConn io.ReadWriteCloser, localIP string) {
	// bind a local udp port
	local, err := net.ListenUDP("udp", common.BuildUDPBindAddr(localIP))
	defer serverConn.Close()
	if err != nil {
		s.logError("bind local udp port error %s", err.Error())
		return
	}
	defer local.Close()
	go func() {
		defer serverConn.Close()
		b := common.BufPool.Get().([]byte)
		defer common.BufPool.Put(b)
		var buf bytes.Buffer
		for {
			n, raddr, err := local.ReadFrom(b)
			if err != nil {
				s.logError("read data from remote server error %s", err.Error())
				return
			}
			buf.Reset()
			dgram := common.NewUDPDatagram(common.NewUDPHeader(0, 0, common.ToSocksAddr(raddr)), b[:n])
			dgram.Write(&buf)
			data, err := conn.GetLenBytes(buf.Bytes())
			if err != nil {
				s.logWarn("get len bytes error %s", err.Error())
				continue
			}
			if _, err := serverConn.Write(data); err != nil {
				s.logError("write data to remote  error %s", err.Error())
				return
			}
		}
	}()
	b := common.BufPool.Get().([]byte)
	defer common.BufPool.Put(b)
	for {
		n, err := serverConn.Read(b)
		if err != nil {
			s.logError("read udp data from server error %s", err.Error())
			return
		}

		udpData, err := common.ReadUDPDatagram(bytes.NewReader(b[:n]))
		if err != nil {
			s.logError("unpack data error %s", err.Error())
			return
		}
		raddr, err := net.ResolveUDPAddr("udp", udpData.Header.Addr.String())
		if err != nil {
			s.logError("build remote addr err %s", err.Error())
			continue // drop silently
		}
		_, err = local.WriteTo(udpData.Data, raddr)
		if err != nil {
			s.logError("write data to remote %s error %s", raddr.String(), err.Error())
			return
		}
	}
}

// Whether the monitor channel is closed
func (s *TRPClient) ping() {
	ticker := time.NewTicker(time.Second * 5)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			tunnel := s.currentTunnel()
			if tunnel != nil && tunnel.IsClose() {
				s.Close()
				return
			}
		case <-s.closeCh:
			return
		}
	}
}

func (s *TRPClient) Close() {
	signal, tunnel, detached := s.detach(nil, nil)
	if !detached {
		return
	}
	s.closeDetached(signal, tunnel)
}

func (s *TRPClient) closing() {
	// Kept for source compatibility with older internal callers. Close now
	// detaches resources before entering the once-guarded cleanup path.
	signal, tunnel, _ := s.detach(nil, nil)
	s.closeDetached(signal, tunnel)
}
