package bridge

import (
	"crypto/tls"
	"ehang.io/nps/lib/nps_mux"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/version"
	"ehang.io/nps/server/connection"
	"ehang.io/nps/server/tool"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"
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
				go s.cliProcess(conn.NewConn(c))
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
	if ServerTlsEnable {
		tlsBridgePort := beego.AppConfig.DefaultInt("tls_bridge_port", 8025)
		logs.Info("tls server start, the bridge type is %s, the tls bridge port is %d", "tcp", tlsBridgePort)
		tlsListener, tlsErr := net.Listen("tcp", net.JoinHostPort(strings.TrimSpace(beego.AppConfig.String("bridge_ip")), strconv.Itoa(tlsBridgePort)))
		if tlsErr != nil {
			_ = listener.Close()
			return tlsErr
		}
		go conn.Accept(tlsListener, func(c net.Conn) {
			s.cliProcess(conn.NewConn(tls.Server(c, &tls.Config{Certificates: []tls.Certificate{crypt.GetCert()}, MinVersion: tls.VersionTLS12})))
		})
	}
	go s.ping()
	go conn.Accept(listener, func(c net.Conn) {
		s.cliProcess(conn.NewConn(c))
	})
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
		logs.Trace("clientId %d did not report local addr (old client or timeout): %v", id, err)
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
		logs.Info("clientId %d local addr: %s", id, localAddr)
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

func (s *Bridge) cliProcess(c *conn.Conn) {
	// Bound the handshake so half-open/scanning connections do not retain a goroutine.
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	//read test flag
	if _, err := c.GetShortContent(3); err != nil {
		logs.Info("The client %s connect error", c.Conn.RemoteAddr(), err.Error())
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
		logs.Info("get client %s version error", err.Error())
		c.Close()
		return
	}
	//write server version to client
	c.Write([]byte(crypt.Md5(version.GetVersion())))
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	var buf []byte
	//get vKey from client
	if buf, err = c.GetShortContent(32); err != nil {
		c.Close()
		return
	}
	//verify
	id, err := file.GetDb().GetIdByVerifyKey(string(buf), c.Conn.RemoteAddr().String())
	if err != nil {
		logs.Info("Current client connection validation error, close this client:", c.Conn.RemoteAddr())
		s.verifyError(c)
		return
	} else {
		s.verifySuccess(c)
	}
	if flag, err := c.ReadFlag(); err == nil {
		_ = c.SetReadDeadline(time.Time{})
		s.typeDeal(flag, c, id, string(vs))
	} else {
		logs.Warn(err, flag)
		c.Close()
	}
	return
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
	cl.signal, cl.tunnel, cl.file = nil, nil, nil
	cl.mu.Unlock()
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
	cl.signal, cl.tunnel, cl.file = nil, nil, nil
	cl.mu.Unlock()
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

// use different
func (s *Bridge) typeDeal(typeVal string, c *conn.Conn, id int, vs string) {
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
		if v, ok := s.Client.Load(id); ok {
			if cl, valid := bridgeClient(v); valid {
				cl.mu.Lock()
				oldSignal = cl.signal
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
		s.clientsMu.Unlock()
		if oldSignal != nil && oldSignal != c {
			_ = oldSignal.Close()
		}
		s.requestClientLocalAddr(id, c)
		go s.GetHealthFromClient(id, c)
		logs.Info("clientId %d connection succeeded, address:%s ", id, c.Conn.RemoteAddr())
	case common.WORK_CHAN:
		muxConn := nps_mux.NewMux(c.Conn, s.tunnelType, s.disconnectTime)
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
		muxConn := nps_mux.NewMux(c.Conn, s.tunnelType, s.disconnectTime)
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
	c.SetAlive(s.tunnelType)
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
		return
	}
	if v, ok := s.Client.Load(clientId); ok {
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
				logs.Info("the client %d closed", v.id)
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
