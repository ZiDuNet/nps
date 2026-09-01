package proxy

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego/logs"
)

const (
	ipV4            = 1
	domainName      = 3
	ipV6            = 4
	connectMethod   = 1
	bindMethod      = 2
	associateMethod = 3
	// The maximum packet size of any udp Associate packet, based on ethernet's max size,
	// minus the IP and UDP headers. IPv4 has a 20 byte header, UDP adds an
	// additional 4 bytes.  This is a total overhead of 24 bytes.  Ethernet's
	// max packet size is 1500 bytes,  1500 - 24 = 1476.
	maxUDPPacketSize = 1476
)

const (
	succeeded uint8 = iota
	serverFailure
	notAllowed
	networkUnreachable
	hostUnreachable
	connectionRefused
	ttlExpired
	commandNotSupported
	addrTypeNotSupported
)

const (
	UserPassAuth    = uint8(2)
	userAuthVersion = uint8(1)
	authSuccess     = uint8(0)
	authFailure     = uint8(1)
)

var socks5HandshakeTimeout = 10 * time.Second

type Sock5ModeServer struct {
	BaseServer
	listener   net.Listener
	listenerMu sync.RWMutex
	closed     bool
}

type socksTaskSnapshot struct {
	bridgeTask   *file.Tunnel
	client       *file.Client
	clientConfig file.Config
	clientFlow   *file.Flow
	taskFlow     *file.Flow
	localProxy   bool
	serverIP     string
	multiAccount map[string]string
}

func (s *Sock5ModeServer) snapshotTask() (socksTaskSnapshot, error) {
	var snapshot socksTaskSnapshot
	if s == nil || s.task == nil || s.bridge == nil {
		return snapshot, errors.New("socks5 server is not configured")
	}
	s.task.RLock()
	snapshot.client = s.task.Client
	snapshot.taskFlow = s.task.Flow
	snapshot.serverIP = s.task.ServerIp
	snapshot.bridgeTask = &file.Tunnel{Mode: s.task.Mode}
	target := s.task.Target
	if s.task.MultiAccount != nil {
		snapshot.multiAccount = make(map[string]string, len(s.task.MultiAccount.AccountMap))
		for username, password := range s.task.MultiAccount.AccountMap {
			snapshot.multiAccount[username] = password
		}
	}
	s.task.RUnlock()
	if target != nil {
		target.RLock()
		snapshot.localProxy = target.LocalProxy
		target.RUnlock()
	}
	if snapshot.client == nil {
		return socksTaskSnapshot{}, errors.New("socks5 task client is not configured")
	}
	snapshot.client.RLock()
	if snapshot.client.Cnf == nil {
		snapshot.client.RUnlock()
		return socksTaskSnapshot{}, errors.New("socks5 task client is not configured")
	}
	snapshot.clientConfig = *snapshot.client.Cnf
	snapshot.clientFlow = snapshot.client.Flow
	snapshot.client.RUnlock()
	if snapshot.serverIP == "" {
		snapshot.serverIP = "0.0.0.0"
	}
	return snapshot, nil
}

// reserveClientForRequest performs the checks that must happen before a
// SOCKS request can allocate an NPC stream.  Unlike regular SOCKS CONNECT,
// UDP ASSOCIATE does not go through DealClient, so keeping this at the
// protocol boundary prevents it from bypassing client IP policy or quotas.
func (s *Sock5ModeServer) reserveClientForRequest(c net.Conn) (*file.Client, error) {
	snapshot, err := s.snapshotTask()
	if err != nil {
		return nil, err
	}
	return s.reserveClientForSnapshot(c, snapshot)
}

func (s *Sock5ModeServer) reserveClientForSnapshot(c net.Conn, snapshot socksTaskSnapshot) (*file.Client, error) {
	if c == nil || c.RemoteAddr() == nil {
		return nil, errors.New("socks5 connection has no remote address")
	}

	client := snapshot.client
	remoteAddr := c.RemoteAddr().String()
	if IsGlobalBlackIp(remoteAddr) || isClientBlackBlocked(client, remoteAddr) || isIPWhiteBlocked(client, remoteAddr) {
		return nil, errors.New("socks5 client IP is not authorized")
	}
	if err := s.CheckFlowAndConnNum(client); err != nil {
		return nil, err
	}
	return client, nil
}

// req
func (s *Sock5ModeServer) handleRequest(c net.Conn) {
	snapshot, err := s.snapshotTask()
	if err != nil {
		_ = c.Close()
		return
	}
	s.handleRequestWithSnapshot(c, snapshot)
}

func (s *Sock5ModeServer) handleRequestWithSnapshot(c net.Conn, snapshot socksTaskSnapshot) {
	/*
		The SOCKS request is formed as follows:
		+----+-----+-------+------+----------+----------+
		|VER | CMD |  RSV  | ATYP | DST.ADDR | DST.PORT |
		+----+-----+-------+------+----------+----------+
		| 1  |  1  | X'00' |  1   | Variable |    2     |
		+----+-----+-------+------+----------+----------+
	*/
	header := make([]byte, 3)

	_, err := io.ReadFull(c, header)

	if err != nil {
		logs.Warn("illegal request", err)
		c.Close()
		return
	}
	if header[0] != 5 || header[2] != 0 {
		logs.Warn("invalid socks5 request header from %s", c.RemoteAddr())
		_ = c.Close()
		return
	}

	switch header[1] {
	case connectMethod:
		s.doConnectWithSnapshot(c, connectMethod, snapshot)
	case bindMethod:
		s.handleBind(c)
	case associateMethod:
		s.handleUDPWithSnapshot(c, snapshot)
	default:
		s.sendReply(c, commandNotSupported)
		c.Close()
	}
}

// reply
func (s *Sock5ModeServer) sendReply(c net.Conn, rep uint8) {
	reply := []byte{
		5,
		rep,
		0,
		1,
	}

	localAddr := c.LocalAddr().String()
	localHost, localPort, _ := net.SplitHostPort(localAddr)
	ip := net.ParseIP(localHost)
	if ip == nil {
		ip = net.ParseIP("0.0.0.0")
	}
	ipBytes := ip.To4()
	if ipBytes == nil {
		ipBytes = net.ParseIP("0.0.0.0").To4()
	}
	nPort, _ := strconv.Atoi(localPort)
	reply = append(reply, ipBytes...)
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(nPort))
	reply = append(reply, portBytes...)

	c.Write(reply)
}

// do conn
func (s *Sock5ModeServer) doConnect(c net.Conn, command uint8) {
	snapshot, err := s.snapshotTask()
	if err != nil {
		_ = c.Close()
		return
	}
	s.doConnectWithSnapshot(c, command, snapshot)
}

func (s *Sock5ModeServer) doConnectWithSnapshot(c net.Conn, command uint8, snapshot socksTaskSnapshot) {
	if c == nil {
		return
	}
	defer c.Close()

	client, err := s.reserveClientForSnapshot(c, snapshot)
	if err != nil {
		logs.Warn("reject socks5 request from %s: %s", c.RemoteAddr(), err)
		return
	}
	defer client.AddConn()

	addrType := make([]byte, 1)
	if _, err := io.ReadFull(c, addrType); err != nil {
		return
	}
	var host string
	switch addrType[0] {
	case ipV4:
		ipv4 := make(net.IP, net.IPv4len)
		if _, err := io.ReadFull(c, ipv4); err != nil {
			return
		}
		host = ipv4.String()
	case ipV6:
		ipv6 := make(net.IP, net.IPv6len)
		if _, err := io.ReadFull(c, ipv6); err != nil {
			return
		}
		host = ipv6.String()
	case domainName:
		var domainLen uint8
		if err := binary.Read(c, binary.BigEndian, &domainLen); err != nil {
			return
		}
		domain := make([]byte, domainLen)
		if _, err := io.ReadFull(c, domain); err != nil {
			return
		}
		host = string(domain)
	default:
		s.sendReply(c, addrTypeNotSupported)
		return
	}

	var port uint16
	if err := binary.Read(c, binary.BigEndian, &port); err != nil {
		return
	}
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		return
	}
	// connect to host
	addr := net.JoinHostPort(host, strconv.Itoa(int(port)))
	var ltype string
	if command == associateMethod {
		ltype = common.CONN_UDP
	} else {
		ltype = common.CONN_TCP
	}
	s.DealClient(conn.NewConn(c), client, addr, nil, ltype, func() {
		s.sendReply(c, succeeded)
	}, snapshot.taskFlow, snapshot.localProxy, nil, nil)
	return
}

// conn
func (s *Sock5ModeServer) handleConnect(c net.Conn) {
	snapshot, err := s.snapshotTask()
	if err != nil {
		_ = c.Close()
		return
	}
	s.doConnectWithSnapshot(c, connectMethod, snapshot)
}

// passive mode
func (s *Sock5ModeServer) handleBind(c net.Conn) {
	s.sendReply(c, commandNotSupported)
	c.Close()
}
func (s *Sock5ModeServer) sendUdpReply(writeConn net.Conn, c net.Conn, rep uint8, serverIp string) error {
	reply := []byte{
		5,
		rep,
		0,
		1,
	}
	localHost, localPort, _ := net.SplitHostPort(c.LocalAddr().String())
	localHost = serverIp
	ip := net.ParseIP(localHost)
	if ip == nil {
		ip = net.ParseIP("0.0.0.0")
	}
	ipBytes := ip.To4()
	if ipBytes == nil {
		ipBytes = net.ParseIP("0.0.0.0").To4()
	}
	nPort, _ := strconv.Atoi(localPort)
	reply = append(reply, ipBytes...)
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(nPort))
	reply = append(reply, portBytes...)
	n, err := writeConn.Write(reply)
	if err != nil {
		return err
	}
	if n != len(reply) {
		return io.ErrShortWrite
	}
	return nil
}

func (s *Sock5ModeServer) handleUDP(c net.Conn) {
	snapshot, err := s.snapshotTask()
	if err != nil {
		_ = c.Close()
		return
	}
	s.handleUDPWithSnapshot(c, snapshot)
}

func (s *Sock5ModeServer) handleUDPWithSnapshot(c net.Conn, snapshot socksTaskSnapshot) {
	if c == nil {
		return
	}
	defer c.Close()

	client, err := s.reserveClientForSnapshot(c, snapshot)
	if err != nil {
		logs.Warn("reject socks5 UDP associate from %s: %s", c.RemoteAddr(), err)
		return
	}
	defer client.AddConn()
	clientConfig := snapshot.clientConfig

	addrType := make([]byte, 1)
	if _, err := io.ReadFull(c, addrType); err != nil {
		return
	}
	var host string
	switch addrType[0] {
	case ipV4:
		ipv4 := make(net.IP, net.IPv4len)
		if _, err := io.ReadFull(c, ipv4); err != nil {
			return
		}
		host = ipv4.String()
	case ipV6:
		ipv6 := make(net.IP, net.IPv6len)
		if _, err := io.ReadFull(c, ipv6); err != nil {
			return
		}
		host = ipv6.String()
	case domainName:
		var domainLen uint8
		if err := binary.Read(c, binary.BigEndian, &domainLen); err != nil {
			return
		}
		domain := make([]byte, domainLen)
		if _, err := io.ReadFull(c, domain); err != nil {
			return
		}
		host = string(domain)
	default:
		s.sendReply(c, addrTypeNotSupported)
		return
	}
	//读取端口
	var port uint16
	if err := binary.Read(c, binary.BigEndian, &port); err != nil {
		return
	}
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		return
	}
	logs.Warn(host, strconv.Itoa(int(port)))
	replyAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(snapshot.serverIP, "0"))
	if err != nil {
		logs.Error("build local reply addr error", err)
		return
	}
	reply, err := net.ListenUDP("udp", replyAddr)
	if err != nil {
		s.sendReply(c, addrTypeNotSupported)
		logs.Error("listen local reply udp port error")
		return
	}
	defer reply.Close()
	// new a tunnel to client
	link := conn.NewLink("udp5", "", clientConfig.Crypt, clientConfig.Compress, c.RemoteAddr().String(), false, "")
	target, err := s.bridge.SendLinkInfo(client.Id, link, snapshot.bridgeTask)
	if err != nil {
		if target != nil {
			_ = target.Close()
		}
		logs.Warn("get connection from client id %d  error %s", client.Id, err.Error())
		return
	}
	if target == nil {
		logs.Warn("get nil connection from client id %d", client.Id)
		return
	}
	defer target.Close()

	// A SOCKS success reply means the bridge is already available.  Sending it
	// earlier leaves clients with a seemingly valid UDP relay that cannot carry
	// traffic when the client is offline.
	remoteTCPAddr, _ := c.RemoteAddr().(*net.TCPAddr)
	serverIP := "0.0.0.0"
	if remoteTCPAddr != nil {
		serverIP = common.GetServerIpByClientIp(remoteTCPAddr.IP)
	}
	if err := s.sendUdpReply(c, reply, succeeded, serverIP); err != nil {
		logs.Warn("send SOCKS5 UDP associate reply to %s: %s", c.RemoteAddr(), err)
		return
	}

	var clientAddr net.Addr
	var clientAddrMu sync.Mutex
	// copy buffer
	go func() {
		b := common.BufPoolUdp.Get().([]byte)
		defer common.BufPoolUdp.Put(b)
		defer c.Close()

		for {
			n, laddr, err := reply.ReadFrom(b)
			if err != nil {
				logs.Error("read data from %s err %s", reply.LocalAddr().String(), err.Error())
				return
			}
			clientAddrMu.Lock()
			if clientAddr == nil {
				clientAddr = laddr
			}
			clientAddrMu.Unlock()
			if _, err := target.Write(b[:n]); err != nil {
				logs.Error("write data to client error", err.Error())
				return
			}
		}
	}()

	go func() {
		var l int32
		b := common.BufPoolUdp.Get().([]byte)
		defer common.BufPoolUdp.Put(b)
		defer c.Close()
		for {
			if err := binary.Read(target, binary.LittleEndian, &l); err != nil || l >= common.PoolSizeUdp || l <= 0 {
				if err != nil {
					logs.Warn("read len bytes error", err.Error())
				} else {
					logs.Warn("invalid UDP packet length %d", l)
				}
				return
			}
			if err := binary.Read(target, binary.LittleEndian, b[:l]); err != nil {
				logs.Warn("read data form client error", err.Error())
				return
			}
			clientAddrMu.Lock()
			addr := clientAddr
			clientAddrMu.Unlock()
			if addr == nil {
				continue
			}
			if _, err := reply.WriteTo(b[:l], addr); err != nil {
				logs.Warn("write data to user ", err.Error())
				return
			}
		}
	}()

	b := common.BufPoolUdp.Get().([]byte)
	defer common.BufPoolUdp.Put(b)
	for {
		_, err := io.ReadFull(c, b)
		if err != nil {
			c.Close()
			return
		}
	}
}

// new conn
func (s *Sock5ModeServer) handleConn(c net.Conn) {
	if c == nil || s == nil {
		if c != nil {
			_ = c.Close()
		}
		return
	}
	if err := c.SetReadDeadline(time.Now().Add(socks5HandshakeTimeout)); err != nil {
		_ = c.Close()
		return
	}
	taskSnapshot, snapshotErr := s.snapshotTask()
	if snapshotErr != nil {
		_ = c.Close()
		return
	}
	clientConfig := taskSnapshot.clientConfig
	buf := make([]byte, 2)
	if _, err := io.ReadFull(c, buf); err != nil {
		logs.Warn("negotiation err", err)
		c.Close()
		return
	}

	if version := buf[0]; version != 5 {
		logs.Warn("only support socks5, request from: ", c.RemoteAddr())
		c.Close()
		return
	}
	nMethods := int(buf[1])
	if nMethods == 0 {
		logs.Warn("socks5 client offered no auth methods, remote %s", c.RemoteAddr())
		c.Close()
		return
	}

	methods := make([]byte, nMethods)
	if _, err := io.ReadFull(c, methods); err != nil {
		logs.Warn("wrong method")
		c.Close()
		return
	}
	needAuth := (clientConfig.U != "" && clientConfig.P != "") || len(taskSnapshot.multiAccount) > 0
	if needAuth {
		offered := false
		for _, method := range methods {
			if method == UserPassAuth {
				offered = true
				break
			}
		}
		if !offered {
			_, _ = c.Write([]byte{5, 0xff})
			c.Close()
			return
		}
		if _, err := c.Write([]byte{5, UserPassAuth}); err != nil {
			c.Close()
			return
		}
		if err := s.authWithSnapshot(c, taskSnapshot); err != nil {
			c.Close()
			logs.Warn("Validation failed:", err)
			return
		}
	} else {
		if _, err := c.Write([]byte{5, 0}); err != nil {
			c.Close()
			return
		}
	}
	s.handleRequestWithSnapshot(c, taskSnapshot)
}

// socks5 auth
func (s *Sock5ModeServer) Auth(c net.Conn) error {
	if c == nil || s == nil {
		return errors.New("socks5 task client is not configured")
	}
	taskSnapshot, snapshotErr := s.snapshotTask()
	if snapshotErr != nil {
		return snapshotErr
	}
	return s.authWithSnapshot(c, taskSnapshot)
}

func (s *Sock5ModeServer) authWithSnapshot(c net.Conn, taskSnapshot socksTaskSnapshot) error {
	if err := c.SetReadDeadline(time.Now().Add(socks5HandshakeTimeout)); err != nil {
		return err
	}
	header := []byte{0, 0}
	if _, err := io.ReadAtLeast(c, header, 2); err != nil {
		return err
	}
	if header[0] != userAuthVersion {
		return errors.New("验证方式不被支持")
	}
	userLen := int(header[1])
	user := make([]byte, userLen)
	if _, err := io.ReadAtLeast(c, user, userLen); err != nil {
		return err
	}
	if _, err := io.ReadFull(c, header[:1]); err != nil {
		return errors.New("密码长度获取错误")
	}
	passLen := int(header[0])
	pass := make([]byte, passLen)
	if _, err := io.ReadAtLeast(c, pass, passLen); err != nil {
		return err
	}

	var U, P string
	if len(taskSnapshot.multiAccount) > 0 {
		// enable multi user auth
		U = string(user)
		if len(U) == 0 {
			return errors.New("验证不通过")
		}
		var ok bool
		P, ok = taskSnapshot.multiAccount[U]
		if !ok {
			return errors.New("验证不通过")
		}
	} else {
		U = taskSnapshot.clientConfig.U
		P = taskSnapshot.clientConfig.P
	}

	if string(user) == U && string(pass) == P {
		if _, err := c.Write([]byte{userAuthVersion, authSuccess}); err != nil {
			return err
		}
		return nil
	} else {
		if _, err := c.Write([]byte{userAuthVersion, authFailure}); err != nil {
			return err
		}
		return errors.New("验证不通过")
	}
}

// start
func (s *Sock5ModeServer) Start() error {
	taskSnapshot, err := s.snapshotTask()
	if err != nil {
		return err
	}
	s.listenerMu.RLock()
	closed := s.closed
	s.listenerMu.RUnlock()
	if closed {
		return net.ErrClosed
	}
	s.task.RLock()
	port := s.task.Port
	s.task.RUnlock()
	listener, err := net.Listen("tcp", net.JoinHostPort(taskSnapshot.serverIP, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	s.listenerMu.Lock()
	if s.closed {
		s.listenerMu.Unlock()
		_ = listener.Close()
		return net.ErrClosed
	}
	s.listener = listener
	s.listenerMu.Unlock()
	conn.Accept(listener, func(c net.Conn) {
		// SOCKS consumes a slot when a CONNECT/UDP ASSOCIATE request is accepted,
		// not when a TCP handshake arrives. Reserving it here as well would count
		// every request twice and make MaxConn=1 unusable.
		currentSnapshot, snapshotErr := s.snapshotTask()
		if snapshotErr != nil {
			_ = c.Close()
			return
		}
		client := currentSnapshot.client
		client.RLock()
		clientID := client.Id
		client.RUnlock()
		logs.Trace("New socks5 connection,client %d,remote address %s", clientID, c.RemoteAddr())
		s.handleConn(c)
	})
	return nil
}

// new
func NewSock5ModeServer(bridge NetBridge, task *file.Tunnel) *Sock5ModeServer {
	s := new(Sock5ModeServer)
	s.bridge = bridge
	s.task = task
	return s
}

// close
func (s *Sock5ModeServer) Close() error {
	if s == nil {
		return nil
	}
	s.listenerMu.Lock()
	s.closed = true
	listener := s.listener
	s.listener = nil
	s.listenerMu.Unlock()
	if listener == nil {
		return nil
	}
	return listener.Close()
}
