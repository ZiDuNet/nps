package client

import (
	"bufio"
	"bytes"
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
)

type TRPClient struct {
	svrAddr        string
	bridgeConnType string
	proxyUrl       string
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
	closed         atomic.Bool
	closeCh        chan struct{}   // closed when client is shutting down; stops ping
	logger         *logs.BeeLogger // 每个客户端独立的 logger
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
	return &TRPClient{
		svrAddr:        svraddr,
		tlsEnable:      tlsEnable,
		tlsOptions:     options,
		p2pAddr:        make(map[string]string, 0),
		vKey:           vKey,
		bridgeConnType: bridgeConnType,
		proxyUrl:       proxyUrl,
		cnf:            cnf,
		disconnectTime: disconnectTime,
		once:           sync.Once{},
		closeCh:        make(chan struct{}),
		logger:         nil, // 默认使用全局 logger，可通过 SetLogger 设置
	}
}

// SetLogger 设置客户端的独立 logger
func (s *TRPClient) SetLogger(logger *logs.BeeLogger) {
	s.logger = logger
}

// log 辅助方法：如果设置了独立 logger 就使用，否则使用全局 logger
func (s *TRPClient) logInfo(format string, v ...interface{}) {
	if s.logger != nil {
		s.logger.Info(format, v...)
	} else {
		logs.Info(format, v...)
	}
}

func (s *TRPClient) logError(format string, v ...interface{}) {
	if s.logger != nil {
		s.logger.Error(format, v...)
	} else {
		logs.Error(format, v...)
	}
}

func (s *TRPClient) logWarn(format string, v ...interface{}) {
	if s.logger != nil {
		s.logger.Warn(format, v...)
	} else {
		logs.Warn(format, v...)
	}
}

func (s *TRPClient) logTrace(format string, v ...interface{}) {
	if s.logger != nil {
		s.logger.Trace(format, v...)
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
	s.once.Do(func() {
		s.closed.Store(true)
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
		s.logError("The connection server failed and will be reconnected in five seconds, error", err.Error())
		if !s.waitReconnect() {
			return
		}
		goto retry
	}
	if c == nil {
		s.logError("Error data from server, and will be reconnected in five seconds")
		if !s.waitReconnect() {
			return
		}
		goto retry
	}
	if s.closed.Load() {
		_ = c.Close()
		return
	}
	s.logInfo("Successful connection with server %s", s.svrAddr)
	//monitor the connection
	go s.ping()
	if !s.installSignal(c) {
		_ = c.Close()
		return
	}
	//start a channel connection
	go s.newChan()
	//start health check if the it's open
	if s.cnf != nil && len(s.cnf.Healths) > 0 {
		go heathCheck(s.cnf.Healths, c, s.closeCh)
	}
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
			s.logError("Accept server data error %s, end this service", err.Error())
			break
		}
		switch flags {
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
				s.logWarn(err.Error())
				break mainLoop
			} else if pwd, err := signal.GetShortLenContent(); err == nil {
				var localAddr string
				//The local port remains unchanged for a certain period of time
				if v, ok := s.p2pAddr[crypt.Md5(string(pwd)+strconv.Itoa(int(time.Now().Unix()/100)))]; !ok {
					tmpConn, err := common.GetLocalUdpAddr()
					if err != nil {
						s.logError(err.Error())
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
		s.logError(err.Error())
		return
	}
	l, err := kcp.ServeConn(nil, 150, 3, localConn)
	if err != nil {
		_ = localConn.Close()
		s.logError(err.Error())
		return
	}
	defer l.Close()
	s.logTrace("start local p2p udp listen, local address %s", localConn.LocalAddr().String())
	for {
		udpTunnel, err := l.AcceptKCP()
		if err != nil {
			s.logError(err.Error())
			return
		}
		if !matchesP2PRemote(udpTunnel, remoteAddress) {
			continue
		}
		conn.SetUdpSession(udpTunnel)
		s.logTrace("successful connection with client ,address %s", udpTunnel.RemoteAddr().String())
		//read link info from remote
		conn.Accept(nps_mux.NewMux(udpTunnel, s.bridgeConnType, s.disconnectTime), func(c net.Conn) {
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
		s.logError("connect to %s error: %v, client will reconnect", s.svrAddr, err)
		s.Close()
		return
	}
	newMux := nps_mux.NewMux(tunnel.Conn, s.bridgeConnType, s.disconnectTime)
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
			s.logWarn(err.Error())
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
		s.logError("get connection info from server error %v", err)
		return
	}
	//host for target processing
	lk.Host = common.FormatAddress(lk.Host)
	//if Conn type is http, read the request and log
	if lk.ConnType == "http" {
		if targetConn, err := net.DialTimeout(common.CONN_TCP, lk.Host, lk.Option.Timeout); err != nil {
			s.logWarn("connect to %s error %s", lk.Host, err.Error())
			src.Close()
		} else {
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
		s.handleUdp(conn.GetConn(src, lk.Crypt, lk.Compress, nil, false, lk.TLSFingerprint))
		return
	}
	//connect to target if conn type is tcp or udp
	if targetConn, err := net.DialTimeout(lk.ConnType, lk.Host, lk.Option.Timeout); err != nil {
		s.logWarn("connect to %s error %s", lk.Host, err.Error())
		src.Close()
	} else {
		s.logTrace("new %s connection with the goal of %s, remote address:%s", lk.ConnType, lk.Host, lk.RemoteAddr)

		if lk.ProtoVersion == "V1" || lk.ProtoVersion == "V2" {
			var addr = targetConn.RemoteAddr()
			if lk.RemoteAddr != "" {
				if parsed, parseErr := net.ResolveTCPAddr("tcp", lk.RemoteAddr); parseErr == nil {
					addr = parsed
				} else {
					s.logWarn("invalid remote address %q for PROXY header: %v", lk.RemoteAddr, parseErr)
				}
			}

			var version byte

			if lk.ProtoVersion == "V1" {
				version = 1
			} else if lk.ProtoVersion == "V2" {
				version = 2
			}

			transportProtocol := proxyproto.TCPv4
			if strings.Contains(addr.String(), ".") {
				transportProtocol = proxyproto.TCPv4
			} else {
				transportProtocol = proxyproto.TCPv6
			}

			header := &proxyproto.Header{
				Command:           proxyproto.PROXY,
				SourceAddr:        addr,
				DestinationAddr:   targetConn.RemoteAddr(),
				Version:           version,
				TransportProtocol: transportProtocol,
			}

			_, err2 := header.WriteTo(targetConn)
			if err2 != nil {
				s.logError(err2.Error())
			}
		}

		conn.CopyWaitGroup(src, targetConn, lk.Crypt, lk.Compress, nil, nil, false, nil, nil, nil, lk.TLSFingerprint)
	}
}

func (s *TRPClient) handleUdp(serverConn io.ReadWriteCloser) {
	// bind a local udp port
	local, err := net.ListenUDP("udp", nil)
	defer serverConn.Close()
	if err != nil {
		s.logError("bind local udp port error %s", err.Error())
		return
	}
	defer local.Close()
	go func() {
		defer serverConn.Close()
		b := common.BufPoolUdp.Get().([]byte)
		defer common.BufPoolUdp.Put(b)
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
	b := common.BufPoolUdp.Get().([]byte)
	defer common.BufPoolUdp.Put(b)
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
