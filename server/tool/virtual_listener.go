package tool

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
)

const virtualListenerQueueSize = 1024

// VirtualListener is an in-process net.Listener used to route a connection
// through an existing HTTP or Bridge handler without opening another socket.
// The address supplied to Dial is preserved as the accepted connection's
// RemoteAddr, which keeps source-IP checks and request logging meaningful.
type VirtualListener struct {
	addr  net.Addr
	queue chan net.Conn
	done  chan struct{}
	once  sync.Once
	mu    sync.Mutex
	close bool
}

type virtualAddressConn struct {
	net.Conn
	local  net.Addr
	remote net.Addr
}

func (c *virtualAddressConn) LocalAddr() net.Addr  { return c.local }
func (c *virtualAddressConn) RemoteAddr() net.Addr { return c.remote }

// NewVirtualListener creates an in-memory listener. A nil address is exposed
// as a loopback TCP address with port zero; it is descriptive only and is
// never bound on the host.
func NewVirtualListener(addr net.Addr) *VirtualListener {
	if addr == nil {
		addr = &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}
	}
	return &VirtualListener{
		addr:  addr,
		queue: make(chan net.Conn, virtualListenerQueueSize),
		done:  make(chan struct{}),
	}
}

func (l *VirtualListener) Accept() (net.Conn, error) {
	if l == nil {
		return nil, net.ErrClosed
	}
	select {
	case <-l.done:
		return nil, net.ErrClosed
	default:
	}
	select {
	case <-l.done:
		return nil, net.ErrClosed
	case c := <-l.queue:
		if c == nil {
			return nil, net.ErrClosed
		}
		return c, nil
	}
}

func (l *VirtualListener) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		l.mu.Lock()
		l.close = true
		close(l.done)
		for {
			select {
			case c := <-l.queue:
				if c != nil {
					_ = c.Close()
				}
			default:
				l.mu.Unlock()
				return
			}
		}
	})
	return nil
}

func (l *VirtualListener) Addr() net.Addr {
	if l == nil {
		return nil
	}
	return l.addr
}

// Dial connects an in-process caller to this listener. remote may be empty;
// when present it must be a host:port address.
func (l *VirtualListener) Dial(ctx context.Context, remote string) (net.Conn, error) {
	if l == nil {
		return nil, net.ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	remote = strings.TrimSpace(remote)
	var remoteAddr net.Addr = &net.TCPAddr{}
	if remote != "" {
		resolved, err := net.ResolveTCPAddr("tcp", remote)
		if err != nil {
			return nil, err
		}
		remoteAddr = resolved
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	caller, accepted := net.Pipe()
	caller = &virtualAddressConn{Conn: caller, local: remoteAddr, remote: l.addr}
	accepted = &virtualAddressConn{Conn: accepted, local: l.addr, remote: remoteAddr}
	l.mu.Lock()
	if l.close {
		l.mu.Unlock()
		_ = caller.Close()
		_ = accepted.Close()
		return nil, net.ErrClosed
	}
	select {
	case l.queue <- accepted:
		l.mu.Unlock()
		return caller, nil
	default:
		l.mu.Unlock()
		_ = caller.Close()
		_ = accepted.Close()
		return nil, errors.New("virtual listener queue is full")
	}
}

// Serve transfers an already-established stream (for example, a WebSocket
// stream) into this listener. It returns false when the listener is closed or
// overloaded; in either case ownership stays with the caller.
func (l *VirtualListener) Serve(c net.Conn) bool {
	if l == nil || c == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.close {
		return false
	}
	select {
	case l.queue <- c:
		return true
	default:
		return false
	}
}

var virtualApplicationListeners struct {
	sync.RWMutex
	web   *VirtualListener
	ipreg *VirtualListener
}

func SetWebServerListener(listener *VirtualListener) {
	virtualApplicationListeners.Lock()
	virtualApplicationListeners.web = listener
	virtualApplicationListeners.Unlock()
}

func SetIPRegisterListener(listener *VirtualListener) {
	virtualApplicationListeners.Lock()
	virtualApplicationListeners.ipreg = listener
	virtualApplicationListeners.Unlock()
}

func GetWebServerConn(ctx context.Context, remote string) (net.Conn, error) {
	virtualApplicationListeners.RLock()
	listener := virtualApplicationListeners.web
	virtualApplicationListeners.RUnlock()
	if listener == nil {
		return nil, errors.New("web server is not running")
	}
	return listener.Dial(ctx, remote)
}

func GetIPRegisterConn(ctx context.Context, remote string) (net.Conn, error) {
	virtualApplicationListeners.RLock()
	listener := virtualApplicationListeners.ipreg
	virtualApplicationListeners.RUnlock()
	if listener == nil {
		return nil, errors.New("IP registration page is not running")
	}
	return listener.Dial(ctx, remote)
}
