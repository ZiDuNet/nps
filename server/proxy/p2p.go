package proxy

import (
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego/logs"
)

const (
	p2pSessionTTL    = 2 * time.Minute
	p2pSweepInterval = 30 * time.Second
	p2pRoleField     = 1
	p2pPasswordField = 0
)

type P2PServer struct {
	BaseServer
	p2pPort    int
	p2p        map[string]*p2p
	p2pMu      sync.Mutex
	listenerMu sync.RWMutex
	closeOnce  sync.Once
	closeCh    chan struct{}
	closed     bool
	listener   *net.UDPConn
}

type p2p struct {
	visitorAddr  *net.UDPAddr
	providerAddr *net.UDPAddr
	lastSeen     time.Time
}

func NewP2PServer(p2pPort int) *P2PServer {
	return &P2PServer{
		p2pPort: p2pPort,
		p2p:     make(map[string]*p2p),
		closeCh: make(chan struct{}),
	}
}

func (s *P2PServer) Start() error {
	if s == nil {
		return errors.New("p2p server is nil")
	}
	logs.Info("start p2p server port", s.p2pPort)
	var err error
	s.listenerMu.RLock()
	closed := s.closed
	s.listenerMu.RUnlock()
	if closed {
		return net.ErrClosed
	}
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("0.0.0.0"), Port: s.p2pPort})
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
	go s.sweepSessions()
	for {
		buf := common.BufPoolUdp.Get().([]byte)
		n, addr, err := listener.ReadFromUDP(buf)
		if err != nil {
			common.BufPoolUdp.Put(buf)
			if errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "use of closed network connection") {
				break
			}
			continue
		}
		packet := string(buf[:n])
		common.BufPoolUdp.Put(buf)
		go s.handleP2P(addr, packet)
	}
	return nil
}

func (s *P2PServer) handleP2P(addr *net.UDPAddr, str string) {
	var (
		v  *p2p
		ok bool
	)
	arr := strings.Split(str, common.CONN_DATA_SEQ)
	if len(arr) < 2 || !validP2PHandshake(arr) {
		return
	}
	s.p2pMu.Lock()
	if v, ok = s.p2p[arr[0]]; !ok {
		v = &p2p{lastSeen: time.Now()}
		s.p2p[arr[0]] = v
	} else {
		v.lastSeen = time.Now()
	}
	s.p2pMu.Unlock()
	logs.Trace("new p2p connection, role %s, local address %s", arr[1], addr.String())
	if arr[1] == common.WORK_P2P_VISITOR {
		visitorAddr := cloneUDPAddr(addr)
		s.p2pMu.Lock()
		v.visitorAddr = visitorAddr
		providerAddr := cloneUDPAddr(v.providerAddr)
		s.p2pMu.Unlock()
		for i := 20; i > 0; i-- {
			if providerAddr == nil {
				timer := time.NewTimer(time.Second)
				select {
				case <-timer.C:
				case <-s.closeCh:
					timer.Stop()
					return
				}
				s.p2pMu.Lock()
				providerAddr = cloneUDPAddr(v.providerAddr)
				s.p2pMu.Unlock()
				continue
			}
			listener := s.getListener()
			if listener != nil {
				_, _ = listener.WriteTo([]byte(providerAddr.String()), visitorAddr)
				_, _ = listener.WriteTo([]byte(visitorAddr.String()), providerAddr)
			}
			s.p2pMu.Lock()
			if current, exists := s.p2p[arr[0]]; exists && current == v {
				delete(s.p2p, arr[0])
			}
			s.p2pMu.Unlock()
			return
		}
		s.p2pMu.Lock()
		if current, exists := s.p2p[arr[0]]; exists && current == v {
			delete(s.p2p, arr[0])
		}
		s.p2pMu.Unlock()
	} else {
		s.p2pMu.Lock()
		v.providerAddr = cloneUDPAddr(addr)
		s.p2pMu.Unlock()
	}
}

// validP2PHandshake rejects arbitrary UDP datagrams before they can allocate
// an entry in the rendezvous map. The password is the MD5 of a configured,
// active p2p task; role is intentionally limited to the two protocol roles.
func validP2PHandshake(fields []string) bool {
	if len(fields) <= p2pRoleField || fields[p2pPasswordField] == "" {
		return false
	}
	if fields[p2pRoleField] != common.WORK_P2P_VISITOR && fields[p2pRoleField] != common.WORK_P2P_PROVIDER {
		return false
	}
	if len(fields[p2pPasswordField]) != 32 {
		return false
	}
	if _, err := hex.DecodeString(fields[p2pPasswordField]); err != nil {
		return false
	}
	t := file.GetDb().GetTaskByMd5Password(fields[p2pPasswordField])
	if t == nil {
		return false
	}
	t.RLock()
	valid := t.Status && t.Mode == "p2p"
	t.RUnlock()
	return valid
}

func (s *P2PServer) sweepSessions() {
	ticker := time.NewTicker(p2pSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			s.p2pMu.Lock()
			for key, session := range s.p2p {
				if now.Sub(session.lastSeen) > p2pSessionTTL {
					delete(s.p2p, key)
				}
			}
			s.p2pMu.Unlock()
		case <-s.closeCh:
			return
		}
	}
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	copyAddr := *addr
	copyAddr.IP = append(net.IP(nil), addr.IP...)
	return &copyAddr
}

func (s *P2PServer) getListener() *net.UDPConn {
	s.listenerMu.RLock()
	listener := s.listener
	s.listenerMu.RUnlock()
	return listener
}

func (s *P2PServer) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() { close(s.closeCh) })
	s.listenerMu.Lock()
	s.closed = true
	listener := s.listener
	s.listener = nil
	s.listenerMu.Unlock()
	s.p2pMu.Lock()
	s.p2p = make(map[string]*p2p)
	s.p2pMu.Unlock()
	if listener == nil {
		return nil
	}
	return listener.Close()
}
