package client

import (
	"ehang.io/nps/lib/nps_mux"
	"errors"
	"net"
	"net/http"
	"runtime"
	"sync"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/config"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/server/proxy"
	"github.com/astaxie/beego/logs"
	"github.com/xtaci/kcp-go"
)

var (
	LocalServer   []*net.TCPListener
	udpConn       net.Conn
	muxSession    *nps_mux.Mux
	fileServer    []*http.Server
	fileListeners []net.Listener
	localServices []proxy.Service
	p2pNetBridge  *p2pBridge
	lock          sync.RWMutex
	udpConnStatus bool
	localStopCh   chan struct{}
)

type p2pBridge struct {
}

func (p2pBridge *p2pBridge) SendLinkInfo(clientId int, link *conn.Link, t *file.Tunnel) (target net.Conn, err error) {
	var session *nps_mux.Mux
	stopped := false
	for i := 0; i < 20; i++ {
		lock.RLock()
		session = muxSession
		stopped = localStopCh == nil
		lock.RUnlock()
		if stopped || session != nil {
			break
		}
		runtime.Gosched() // waiting for another goroutine establish the mux connection
	}
	if stopped || session == nil {
		err = errors.New("p2pBridge:too many times to get muxSession")
		logs.Error(err)
		return nil, err
	}
	nowConn, err := session.NewConn()
	if err != nil {
		lock.Lock()
		if muxSession == session {
			udpConn = nil
			muxSession = nil
			udpConnStatus = false
		}
		lock.Unlock()
		return nil, err
	}
	if _, err := conn.NewConn(nowConn).SendInfo(link, ""); err != nil {
		_ = nowConn.Close()
		lock.Lock()
		if muxSession == session {
			udpConnStatus = false
		}
		lock.Unlock()
		return nil, err
	}
	return nowConn, nil
}

func CloseLocalServer() {
	lock.Lock()
	listeners := append([]*net.TCPListener(nil), LocalServer...)
	servers := append([]*http.Server(nil), fileServer...)
	fileMuxes := append([]net.Listener(nil), fileListeners...)
	services := append([]proxy.Service(nil), localServices...)
	mux := muxSession
	udp := udpConn
	stop := localStopCh
	LocalServer = nil
	fileServer = nil
	fileListeners = nil
	localServices = nil
	muxSession = nil
	udpConn = nil
	p2pNetBridge = nil
	udpConnStatus = false
	localStopCh = nil
	lock.Unlock()
	if stop != nil {
		close(stop)
	}
	for _, v := range listeners {
		if v != nil {
			_ = v.Close()
		}
	}
	for _, listener := range fileMuxes {
		if listener != nil {
			_ = listener.Close()
		}
	}
	for _, v := range servers {
		if v != nil {
			_ = v.Close()
		}
	}
	for _, service := range services {
		if service != nil {
			_ = service.Close()
		}
	}
	if mux != nil {
		_ = mux.Close()
	}
	if udp != nil {
		_ = udp.Close()
	}
}

func ensureLocalGeneration() <-chan struct{} {
	lock.Lock()
	defer lock.Unlock()
	if localStopCh == nil {
		localStopCh = make(chan struct{})
	}
	return localStopCh
}

// registerLocalListener records a listener only when it belongs to the
// currently active local-server generation. CloseLocalServer can otherwise
// race a late bind and leave an unreachable listener behind.
func registerLocalListener(listener *net.TCPListener, generation <-chan struct{}) bool {
	if listener == nil || generation == nil {
		return false
	}
	lock.Lock()
	defer lock.Unlock()
	if localStopCh != generation {
		return false
	}
	LocalServer = append(LocalServer, listener)
	return true
}

func registerFileServer(server *http.Server, listener net.Listener, generation <-chan struct{}) bool {
	if server == nil || listener == nil || generation == nil {
		return false
	}
	lock.Lock()
	defer lock.Unlock()
	if localStopCh != generation {
		return false
	}
	fileServer = append(fileServer, server)
	fileListeners = append(fileListeners, listener)
	return true
}

func registerLocalService(service proxy.Service, generation <-chan struct{}) bool {
	if service == nil || generation == nil {
		return false
	}
	lock.Lock()
	defer lock.Unlock()
	if localStopCh != generation {
		return false
	}
	localServices = append(localServices, service)
	return true
}

func startLocalFileServer(config *config.CommonConfig, t *file.Tunnel, vkey string, generation <-chan struct{}) {
	if config == nil || t == nil || generation == nil {
		return
	}
	lock.RLock()
	active := localStopCh == generation
	lock.RUnlock()
	if !active {
		return
	}
	remoteConn, err := NewConnWithTLS(config.Tp, vkey, config.Server, common.WORK_FILE, config.ProxyUrl, config.TlsEnable, TLSOptionsFromConfig(config))
	if err != nil {
		logs.Error("Local connection server failed ", err.Error())
		return
	}
	srv := &http.Server{
		Handler: http.StripPrefix(t.StripPre, http.FileServer(http.Dir(t.LocalPath))),
	}
	listener := nps_mux.NewMux(remoteConn.Conn, common.CONN_TCP, config.DisconnectTime)
	logs.Info("start local file system, local path %s, strip prefix %s ,remote port %s ", t.LocalPath, t.StripPre, t.Ports)
	if !registerFileServer(srv, listener, generation) {
		_ = listener.Close()
		_ = remoteConn.Close()
		return
	}
	defer remoteConn.Close()
	if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logs.Error(err)
	}
}

func StartLocalServer(l *config.LocalServer, config *config.CommonConfig) error {
	return startLocalServer(l, config, ensureLocalGeneration())
}

func startLocalServer(l *config.LocalServer, config *config.CommonConfig, stop <-chan struct{}) error {
	if l == nil || config == nil || config.Client == nil || config.Client.Cnf == nil {
		return errors.New("local server configuration is incomplete")
	}
	if stop == nil {
		return errors.New("local server generation is unavailable")
	}
	lock.RLock()
	active := localStopCh == stop
	lock.RUnlock()
	if !active {
		return errors.New("local server stopped during startup")
	}
	if l.Type != "secret" {
		go handleUdpMonitor(config, l, stop)
	}
	task := &file.Tunnel{
		Port:     l.Port,
		ServerIp: "0.0.0.0",
		Status:   true,
		Client: &file.Client{
			Cnf: &file.Config{
				U:        "",
				P:        "",
				Compress: config.Client.Cnf.Compress,
			},
			Status:    true,
			RateLimit: 0,
			Flow:      &file.Flow{},
		},
		Flow:   &file.Flow{},
		Target: &file.Target{},
	}
	lock.RLock()
	bridge := p2pNetBridge
	lock.RUnlock()
	if bridge == nil {
		// The UDP monitor establishes the real mux asynchronously. Keep a
		// stable bridge object in listeners started before that handshake; its
		// SendLinkInfo method resolves the current session on each request.
		bridge = &p2pBridge{}
	}
	switch l.Type {
	case "p2ps":
		logs.Info("successful start-up of local socks5 monitoring, port", l.Port)
		service := proxy.NewSock5ModeServer(bridge, task)
		if !registerLocalService(service, stop) {
			return errors.New("local server stopped during startup")
		}
		if err := service.Start(); err != nil {
			return err
		}
		return nil
	case "p2pt":
		logs.Info("successful start-up of local tcp trans monitoring, port", l.Port)
		service := proxy.NewTunnelModeServer(proxy.HandleTrans, bridge, task)
		if !registerLocalService(service, stop) {
			return errors.New("local server stopped during startup")
		}
		if err := service.Start(); err != nil {
			return err
		}
		return nil
	case "p2p", "secret":
		listener, err := net.ListenTCP("tcp", &net.TCPAddr{
			IP:   net.ParseIP("0.0.0.0"),
			Port: l.Port,
		})
		if err != nil {
			logs.Error("local listener startup failed port %d, error %s", l.Port, err.Error())
			return err
		}
		if !registerLocalListener(listener, stop) {
			_ = listener.Close()
			return errors.New("local server stopped during startup")
		}
		logs.Info("successful start-up of local tcp monitoring, port", l.Port)
		conn.Accept(listener, func(c net.Conn) {
			logs.Trace("new %s connection", l.Type)
			if l.Type == "secret" {
				handleSecret(c, config, l)
			} else if l.Type == "p2p" {
				handleP2PVisitor(c, config, l)
			}
		})
	}
	return nil
}

func handleUdpMonitor(config *config.CommonConfig, l *config.LocalServer, stop <-chan struct{}) {
	ticker := time.NewTicker(time.Second * 1)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			lock.RLock()
			connected := udpConnStatus && udpConn != nil
			lock.RUnlock()
			if !connected {
				lock.Lock()
				udpConn = nil
				udpConnStatus = false
				lock.Unlock()
				tmpConn, err := common.GetLocalUdpAddr()
				if err != nil {
					logs.Error(err)
					return
				}
				for i := 0; i < 10; i++ {
					select {
					case <-stop:
						return
					default:
					}
					logs.Notice("try to connect to the server", i+1)
					newUdpConn(tmpConn.LocalAddr().String(), config, l, stop)
					lock.RLock()
					connected = udpConn != nil && udpConnStatus
					lock.RUnlock()
					if connected {
						break
					}
				}
			}
		}
	}
}

func handleSecret(localTcpConn net.Conn, config *config.CommonConfig, l *config.LocalServer) {
	remoteConn, err := NewConnWithTLS(config.Tp, config.VKey, config.Server, common.WORK_SECRET, config.ProxyUrl, config.TlsEnable, TLSOptionsFromConfig(config))
	if err != nil {
		logs.Error("Local connection server failed ", err.Error())
		return
	}
	if _, err := remoteConn.Write([]byte(crypt.Md5(l.Password))); err != nil {
		_ = remoteConn.Close()
		logs.Error("Local connection server failed ", err.Error())
		return
	}
	defer remoteConn.Close()
	conn.CopyWaitGroup(remoteConn.Conn, localTcpConn, false, false, nil, nil, false, nil, nil, nil)
}

func handleP2PVisitor(localTcpConn net.Conn, config *config.CommonConfig, l *config.LocalServer) {
	lock.RLock()
	udp := udpConn
	bridge := p2pNetBridge
	lock.RUnlock()
	if udp == nil || bridge == nil {
		logs.Notice("new conn, P2P can not penetrate successfully, traffic will be transferred through the server")
		handleSecret(localTcpConn, config, l)
		return
	}
	logs.Trace("start trying to connect with the server")
	//TODO just support compress now because there is not tls file in client packages
	link := conn.NewLink(common.CONN_TCP, l.Target, false, config.Client.Cnf.Compress, localTcpConn.LocalAddr().String(), false, "")
	if target, err := bridge.SendLinkInfo(0, link, nil); err != nil {
		logs.Error(err)
		_ = localTcpConn.Close()
		lock.Lock()
		udpConnStatus = false
		lock.Unlock()
		return
	} else {
		conn.CopyWaitGroup(target, localTcpConn, false, config.Client.Cnf.Compress, nil, nil, false, nil, nil, nil)
	}
}

func newUdpConn(localAddr string, config *config.CommonConfig, l *config.LocalServer, generation <-chan struct{}) {
	if config == nil || config.Client == nil || config.Client.Cnf == nil || l == nil || generation == nil {
		return
	}
	lock.RLock()
	active := localStopCh == generation
	lock.RUnlock()
	if !active {
		return
	}
	remoteConn, err := NewConnWithTLS(config.Tp, config.VKey, config.Server, common.WORK_P2P, config.ProxyUrl, config.TlsEnable, TLSOptionsFromConfig(config))
	if err != nil {
		logs.Error("Local connection server failed ", err.Error())
		return
	}
	if _, err := remoteConn.Write([]byte(crypt.Md5(l.Password))); err != nil {
		_ = remoteConn.Close()
		logs.Error("Local connection server failed ", err.Error())
		return
	}
	var rAddr []byte
	//读取服务端地址、密钥 继续做处理
	if rAddr, err = remoteConn.GetShortLenContent(); err != nil {
		_ = remoteConn.Close()
		logs.Error(err)
		return
	}
	var localConn net.PacketConn
	var remoteAddress string
	if remoteAddress, localConn, err = handleP2PUdp(localAddr, string(rAddr), crypt.Md5(l.Password), common.WORK_P2P_VISITOR); err != nil {
		_ = remoteConn.Close()
		logs.Error(err)
		return
	}
	_ = remoteConn.Close()
	udpTunnel, err := kcp.NewConn(remoteAddress, nil, 150, 3, localConn)
	if err != nil || udpTunnel == nil {
		_ = localConn.Close()
		logs.Warn(err)
		return
	}
	logs.Trace("successful create a connection with server", remoteAddress)
	var oldMux *nps_mux.Mux
	var oldUDP net.Conn
	mux := nps_mux.NewMux(udpTunnel, "kcp", config.DisconnectTime)
	lock.Lock()
	active = localStopCh == generation
	if active {
		conn.SetUdpSession(udpTunnel)
		oldMux = muxSession
		oldUDP = udpConn
		udpConn = udpTunnel
		muxSession = mux
		p2pNetBridge = &p2pBridge{}
		udpConnStatus = true
	}
	lock.Unlock()
	if !active {
		_ = mux.Close()
		_ = udpTunnel.Close()
		_ = localConn.Close()
		return
	}
	if oldMux != nil {
		_ = oldMux.Close()
	}
	if oldUDP != nil && oldUDP != udpTunnel {
		_ = oldUDP.Close()
	}
}
