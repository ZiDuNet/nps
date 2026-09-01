package conn

import (
	"errors"
	"io"
	"net"
	"strings"

	"github.com/astaxie/beego/logs"
	"github.com/xtaci/kcp-go"
)

func NewTcpListenerAndProcess(addr string, f func(c net.Conn), listener *net.Listener) error {
	var err error
	*listener, err = net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	Accept(*listener, f)
	return nil
}

func NewKcpListenerAndProcess(addr string, f func(c net.Conn)) error {
	kcpListener, err := NewKcpListener(addr)
	if err != nil {
		return err
	}
	defer kcpListener.Close()
	for {
		c, err := kcpListener.AcceptKCP()
		if err != nil {
			if c != nil {
				_ = c.Close()
			}
			logs.Warn(err)
			return err
		}
		if c == nil {
			return errors.New("kcp listener returned a nil connection")
		}
		SetUdpSession(c)
		go f(c)
	}
}

// NewKcpListener binds the KCP socket without starting an accept loop. Keeping
// binding separate lets callers report startup errors before launching the
// rest of the server.
func NewKcpListener(addr string) (*kcp.Listener, error) {
	kcpListener, err := kcp.ListenWithOptions(addr, nil, 150, 3)
	if err != nil {
		logs.Error(err)
		return nil, err
	}
	return kcpListener, nil
}

func Accept(l net.Listener, f func(c net.Conn)) {
	if l == nil || f == nil {
		return
	}
	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
				break
			}
			if strings.Contains(err.Error(), "use of closed network connection") {
				break
			}
			if strings.Contains(err.Error(), "the mux has closed") {
				break
			}
			logs.Warn(err)
			continue
		}
		if c == nil {
			logs.Warn("nil connection")
			break
		}
		go f(c)
	}
}
