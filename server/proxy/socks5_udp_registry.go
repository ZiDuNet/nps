package proxy

// This file contains the shared UDP associate implementation used by SOCKS5
// and mixed proxy listeners.  The original implementation created one UDP
// socket per ASSOCIATE request and coupled the socket lifetime to a single
// goroutine.  A registry lets multiple associates share the task port, keeps
// their source addresses separate, and gives the server a bounded lifecycle
// for idle or disconnected clients.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/rate"
	"github.com/astaxie/beego/logs"
)

const (
	socks5UDPAssociateIdleTimeout = 3 * time.Minute
	socks5UDPSweepInterval        = 30 * time.Second
	socks5UDPQueueSize            = 1024
)

type socks5UDPPacket struct {
	data []byte
}

type socks5UDPRegistry struct {
	owner    *Sock5ModeServer
	bridge   NetBridge
	task     *file.Tunnel
	listener *net.UDPConn

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
	wg        sync.WaitGroup

	mu             sync.Mutex
	sessions       map[*socks5UDPSession]struct{}
	sessionsByAddr map[string]*socks5UDPSession
	sessionsByIP   map[string]map[*socks5UDPSession]struct{}
	pendingByIP    map[string][]*socks5UDPSession
}

func newSocks5UDPRegistry(bridge NetBridge, task *file.Tunnel, listener *net.UDPConn) (*socks5UDPRegistry, error) {
	if bridge == nil {
		return nil, errors.New("socks5 UDP registry bridge is nil")
	}
	if task == nil || task.Client == nil {
		return nil, errors.New("socks5 UDP registry task is not configured")
	}
	if listener == nil {
		return nil, errors.New("socks5 UDP registry listener is nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &socks5UDPRegistry{
		bridge:         bridge,
		task:           task,
		listener:       listener,
		ctx:            ctx,
		cancel:         cancel,
		sessions:       make(map[*socks5UDPSession]struct{}),
		sessionsByAddr: make(map[string]*socks5UDPSession),
		sessionsByIP:   make(map[string]map[*socks5UDPSession]struct{}),
		pendingByIP:    make(map[string][]*socks5UDPSession),
	}, nil
}

func (r *socks5UDPRegistry) start() {
	if r == nil {
		return
	}
	r.wg.Add(2)
	go r.readLoop()
	go r.cleanupLoop()
}

func (r *socks5UDPRegistry) LocalAddr() net.Addr {
	if r == nil || r.listener == nil {
		return nil
	}
	return r.listener.LocalAddr()
}

// newSession consumes the address/port portion of the SOCKS5 ASSOCIATE
// request. The command header itself has already been read by handleRequest.
func (r *socks5UDPRegistry) newSession(control net.Conn, snapshot socksTaskSnapshot) error {
	if r == nil || control == nil {
		return errors.New("socks5 UDP control connection is nil")
	}
	if r.owner == nil {
		return errors.New("socks5 UDP registry owner is nil")
	}
	requestedAddr, err := readSOCKS5Address(control)
	if err != nil {
		return err
	}
	if err := control.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	client, err := r.owner.reserveClientForSnapshot(control, snapshot)
	if err != nil {
		return err
	}

	session := &socks5UDPSession{
		registry:      r,
		control:       control,
		client:        client,
		snapshot:      snapshot,
		clientIPKey:   normalizeSocks5UDPIP(control.RemoteAddr()),
		requestedPort: requestedAddr.Port,
		packetCh:      make(chan socks5UDPPacket, socks5UDPQueueSize),
		done:          make(chan struct{}),
	}
	if session.clientIPKey == "" {
		client.AddConn()
		return errors.New("parse socks5 UDP client address failed")
	}
	if err := session.openTunnel(); err != nil {
		client.AddConn()
		return err
	}
	r.registerSession(session)
	session.start()
	if err := r.sendAssociateReply(control, snapshot); err != nil {
		session.Close()
		return err
	}
	return nil
}

func (r *socks5UDPRegistry) registerSession(session *socks5UDPSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[session] = struct{}{}
	if r.sessionsByIP[session.clientIPKey] == nil {
		r.sessionsByIP[session.clientIPKey] = make(map[*socks5UDPSession]struct{})
	}
	r.sessionsByIP[session.clientIPKey][session] = struct{}{}
	r.pendingByIP[session.clientIPKey] = append(r.pendingByIP[session.clientIPKey], session)
}

func (r *socks5UDPRegistry) unregisterSession(session *socks5UDPSession) {
	if r == nil || session == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, session)
	if key, _ := session.boundIdentity(); key != "" {
		if current := r.sessionsByAddr[key]; current == session {
			delete(r.sessionsByAddr, key)
		}
	}
	if byIP := r.sessionsByIP[session.clientIPKey]; byIP != nil {
		delete(byIP, session)
		if len(byIP) == 0 {
			delete(r.sessionsByIP, session.clientIPKey)
		}
	}
	r.pendingByIP[session.clientIPKey] = removeSocks5UDPPending(r.pendingByIP[session.clientIPKey], session)
	if len(r.pendingByIP[session.clientIPKey]) == 0 {
		delete(r.pendingByIP, session.clientIPKey)
	}
}

func (r *socks5UDPRegistry) readLoop() {
	defer r.wg.Done()
	for {
		buf := common.BufPool.Get().([]byte)
		n, addr, err := r.listener.ReadFromUDP(buf)
		if err != nil {
			common.BufPool.Put(buf)
			if r.ctx.Err() != nil || errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "closed network connection") {
				return
			}
			logs.Warn("socks5 UDP listener read error: %v", err)
			continue
		}
		if n <= 0 || addr == nil {
			common.BufPool.Put(buf)
			continue
		}
		session := r.pickSession(addr)
		if session == nil || !session.sourceAllowed(addr) || !session.enqueue(append([]byte(nil), buf[:n]...)) {
			common.BufPool.Put(buf)
			continue
		}
		common.BufPool.Put(buf)
	}
}

func (r *socks5UDPRegistry) pickSession(addr *net.UDPAddr) *socks5UDPSession {
	if r == nil || addr == nil {
		return nil
	}
	key := addr.String()
	ipKey := normalizeSocks5UDPIP(addr)
	if ipKey == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if session := r.sessionsByAddr[key]; session != nil {
		session.bindClientAddr(addr)
		return session
	}
	var session *socks5UDPSession
	if byIP := r.sessionsByIP[ipKey]; len(byIP) == 1 {
		for candidate := range byIP {
			session = candidate
		}
	} else if byIP := r.sessionsByIP[ipKey]; len(byIP) > 1 {
		for candidate := range byIP {
			if !candidate.matchesRequestedPort(addr.Port) {
				continue
			}
			if session != nil {
				session = nil
				break
			}
			session = candidate
		}
		if session == nil {
			if pending := r.pendingByIP[ipKey]; len(pending) == 1 {
				session = pending[0]
			}
		}
	} else if pending := r.pendingByIP[ipKey]; len(pending) == 1 {
		session = pending[0]
	}
	if session == nil {
		return nil
	}
	if old, _ := session.boundIdentity(); old != "" && old != key && r.sessionsByAddr[old] == session {
		delete(r.sessionsByAddr, old)
	}
	r.sessionsByAddr[key] = session
	session.bindClientAddr(addr)
	r.pendingByIP[ipKey] = removeSocks5UDPPending(r.pendingByIP[ipKey], session)
	return session
}

func (r *socks5UDPRegistry) sendAssociateReply(control net.Conn, snapshot socksTaskSnapshot) error {
	addr := r.LocalAddr()
	udpAddr, _ := addr.(*net.UDPAddr)
	if udpAddr == nil {
		return errors.New("socks5 UDP listener address is unavailable")
	}
	serverIP := snapshot.serverIP
	if serverIP == "" || net.ParseIP(serverIP) == nil || net.ParseIP(serverIP).IsUnspecified() {
		remoteIP := net.ParseIP(common.GetIpByAddr(control.RemoteAddr().String()))
		localIP := net.ParseIP(common.GetIpByAddr(control.LocalAddr().String()))
		serverIP = common.GetServerIpByClientIp(remoteIP, localIP)
	}
	ip := net.ParseIP(serverIP)
	if ip == nil {
		ip = net.IPv4zero
	}
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	reply := []byte{5, succeeded, 0}
	if ip4 := ip.To4(); ip4 != nil {
		reply = append(reply, 1)
		reply = append(reply, ip4...)
	} else {
		reply = append(reply, 4)
		reply = append(reply, ip.To16()...)
	}
	port := make([]byte, 2)
	binary.BigEndian.PutUint16(port, uint16(udpAddr.Port))
	reply = append(reply, port...)
	_, err := control.Write(reply)
	return err
}

func (r *socks5UDPRegistry) cleanupLoop() {
	defer r.wg.Done()
	ticker := time.NewTicker(socks5UDPSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case now := <-ticker.C:
			r.mu.Lock()
			sessions := make([]*socks5UDPSession, 0, len(r.sessions))
			for session := range r.sessions {
				if session.idleFor(now) >= socks5UDPAssociateIdleTimeout {
					sessions = append(sessions, session)
				}
			}
			r.mu.Unlock()
			for _, session := range sessions {
				logs.Debug("close idle socks5 UDP associate: %s", session.controlRemoteAddr())
				session.Close()
			}
		}
	}
}

func (r *socks5UDPRegistry) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.cancel()
		if r.listener != nil {
			_ = r.listener.Close()
		}
		r.mu.Lock()
		sessions := make([]*socks5UDPSession, 0, len(r.sessions))
		for session := range r.sessions {
			sessions = append(sessions, session)
		}
		r.mu.Unlock()
		for _, session := range sessions {
			session.Close()
		}
		for _, session := range sessions {
			session.Wait()
		}
		r.wg.Wait()
	})
	return nil
}

type socks5UDPSession struct {
	registry      *socks5UDPRegistry
	control       net.Conn
	client        *file.Client
	snapshot      socksTaskSnapshot
	clientIPKey   string
	requestedPort int

	target     io.ReadWriteCloser
	writeMu    sync.Mutex
	packetCh   chan socks5UDPPacket
	done       chan struct{}
	closeOnce  sync.Once
	wg         sync.WaitGroup
	lastActive atomic.Int64

	boundMu   sync.RWMutex
	boundAddr *net.UDPAddr
	boundKey  string
}

func (s *socks5UDPSession) openTunnel() error {
	if s == nil || s.registry == nil || s.registry.bridge == nil || s.client == nil {
		return errors.New("socks5 UDP session is not configured")
	}
	link := conn.NewLink("udp5", "", s.snapshot.clientConfig.Crypt, s.snapshot.clientConfig.Compress, s.control.RemoteAddr().String(), s.snapshot.localProxy, "")
	link.Option.Timeout = socks5UDPAssociateIdleTimeout
	target, err := s.registry.bridge.SendLinkInfo(s.client.Id, link, s.snapshot.bridgeTask)
	if err != nil {
		if target != nil {
			_ = target.Close()
		}
		return err
	}
	if target == nil {
		return errors.New("bridge returned nil SOCKS5 UDP stream")
	}
	s.target = rate.NewRateConn(target, s.snapshot.clientRate, s.snapshot.taskRate, s.snapshot.userRate)
	return nil
}

func (s *socks5UDPSession) start() {
	s.touch()
	s.wg.Add(3)
	go s.writeLoop()
	go s.readLoop()
	go s.controlLoop()
}

func (s *socks5UDPSession) Wait() { s.wg.Wait() }

func (s *socks5UDPSession) enqueue(data []byte) bool {
	if s == nil || len(data) == 0 {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
	}
	select {
	case s.packetCh <- socks5UDPPacket{data: data}:
		s.touch()
		return true
	default:
		logs.Debug("drop socks5 UDP datagram from %s because queue is full", s.controlRemoteAddr())
		return false
	}
}

func (s *socks5UDPSession) writeLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.done:
			return
		case packet := <-s.packetCh:
			if len(packet.data) == 0 {
				continue
			}
			if !s.destinationAllowed(packet.data) {
				continue
			}
			s.writeMu.Lock()
			_, err := s.target.Write(packet.data)
			s.writeMu.Unlock()
			if err != nil {
				logs.Debug("write SOCKS5 UDP datagram to NPC: %v", err)
				s.Close()
				return
			}
			s.addFlow(int64(len(packet.data)), 0)
			s.touch()
		}
	}
}

func (s *socks5UDPSession) readLoop() {
	defer s.wg.Done()
	buf := common.BufPool.Get().([]byte)
	defer common.BufPool.Put(buf)
	for {
		length, err := readSocks5UDPFrame(s.target)
		if err != nil || length <= 0 || length > len(buf) {
			if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) {
				logs.Debug("read SOCKS5 UDP frame from NPC: %v", err)
			}
			s.Close()
			return
		}
		if _, err := io.ReadFull(s.target, buf[:length]); err != nil {
			s.Close()
			return
		}
		addr := s.clientAddr()
		if addr == nil {
			continue
		}
		if _, err := s.registry.listener.WriteToUDP(buf[:length], addr); err != nil {
			s.Close()
			return
		}
		s.addFlow(0, int64(length))
		s.touch()
	}
}

func (s *socks5UDPSession) controlLoop() {
	defer s.wg.Done()
	defer s.Close()
	var b [1]byte
	_, _ = s.control.Read(b[:])
}

func (s *socks5UDPSession) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		close(s.done)
		if s.registry != nil {
			s.registry.unregisterSession(s)
		}
		if s.target != nil {
			_ = s.target.Close()
		}
		if s.control != nil {
			_ = s.control.Close()
		}
		if s.client != nil {
			s.client.AddConn()
		}
	})
}

func (s *socks5UDPSession) touch() { s.lastActive.Store(time.Now().UnixNano()) }

func (s *socks5UDPSession) idleFor(now time.Time) time.Duration {
	last := s.lastActive.Load()
	if last == 0 {
		return 0
	}
	return now.Sub(time.Unix(0, last))
}

func (s *socks5UDPSession) bindClientAddr(addr *net.UDPAddr) {
	if addr == nil {
		return
	}
	s.boundMu.Lock()
	s.boundAddr = cloneSocks5UDPAddr(addr)
	s.boundKey = addr.String()
	s.boundMu.Unlock()
}

func (s *socks5UDPSession) boundIdentity() (string, *net.UDPAddr) {
	s.boundMu.RLock()
	defer s.boundMu.RUnlock()
	return s.boundKey, cloneSocks5UDPAddr(s.boundAddr)
}

func (s *socks5UDPSession) clientAddr() *net.UDPAddr {
	_, addr := s.boundIdentity()
	return addr
}

func (s *socks5UDPSession) matchesRequestedPort(port int) bool {
	return s != nil && s.requestedPort > 0 && port > 0 && s.requestedPort == port
}

func (s *socks5UDPSession) controlRemoteAddr() string {
	if s == nil || s.control == nil || s.control.RemoteAddr() == nil {
		return "<nil>"
	}
	return s.control.RemoteAddr().String()
}

func (s *socks5UDPSession) sourceAllowed(addr *net.UDPAddr) bool {
	if s == nil || addr == nil || s.client == nil {
		return false
	}
	remote := addr.String()
	if SourceDeniedString(remote, s.client, s.snapshot.sourceLists, "socks5") || IsGlobalBlackIp(remote) || isClientBlackBlocked(s.client, remote) || isRegisteredOnlyBlocked(s.client, remote, s.snapshot.bridgeTask.RegisteredOnly) {
		return false
	}
	return true
}

func (s *socks5UDPSession) destinationAllowed(data []byte) bool {
	if s.snapshot.bridgeTask.Mode != "mixProxy" || s.snapshot.bridgeTask.DestAclMode == file.AclOff {
		return true
	}
	datagram, err := common.ReadUDPDatagram(bytes.NewReader(data))
	if err != nil || datagram == nil || datagram.Header == nil || datagram.Header.Addr == nil {
		return false
	}
	return mixProxyDestinationAllowed(s.snapshot.bridgeTask, datagram.Header.Addr.String())
}

func (s *socks5UDPSession) addFlow(in, out int64) {
	if s == nil {
		return
	}
	if s.snapshot.taskFlow != nil {
		s.snapshot.taskFlow.Add(in, out)
	}
	if s.snapshot.clientFlow != nil {
		s.snapshot.clientFlow.Add(in, out)
	}
}

func readSocks5UDPFrame(r io.Reader) (int, error) {
	var length int32
	if err := binary.Read(r, binary.LittleEndian, &length); err != nil {
		return 0, err
	}
	return int(length), nil
}

func readSOCKS5Address(r io.Reader) (*net.TCPAddr, error) {
	addrType := []byte{0}
	if _, err := io.ReadFull(r, addrType); err != nil {
		return nil, err
	}
	var host string
	switch addrType[0] {
	case ipV4:
		ip := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(r, ip); err != nil {
			return nil, err
		}
		host = net.IP(ip).String()
	case ipV6:
		ip := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(r, ip); err != nil {
			return nil, err
		}
		host = net.IP(ip).String()
	case domainName:
		var length uint8
		if err := binary.Read(r, binary.BigEndian, &length); err != nil {
			return nil, err
		}
		name := make([]byte, length)
		if _, err := io.ReadFull(r, name); err != nil {
			return nil, err
		}
		host = string(name)
	default:
		return nil, errors.New("SOCKS5 address type is not supported")
	}
	var port uint16
	if err := binary.Read(r, binary.BigEndian, &port); err != nil {
		return nil, err
	}
	return &net.TCPAddr{IP: net.ParseIP(host), Port: int(port)}, nil
}

func normalizeSocks5UDPIP(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}
	if ip := net.ParseIP(strings.TrimSpace(host)); ip != nil {
		return ip.String()
	}
	return ""
}

func cloneSocks5UDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	clone := *addr
	clone.IP = append(net.IP(nil), addr.IP...)
	return &clone
}

func removeSocks5UDPPending(sessions []*socks5UDPSession, target *socks5UDPSession) []*socks5UDPSession {
	if len(sessions) == 0 {
		return sessions
	}
	out := sessions[:0]
	for _, session := range sessions {
		if session != target {
			out = append(out, session)
		}
	}
	return out
}
