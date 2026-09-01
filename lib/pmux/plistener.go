package pmux

import (
	"net"
	"sync"
)

type PortListener struct {
	net.Listener
	connCh    chan *PortConn
	addr      net.Addr
	done      chan struct{}
	closeOnce sync.Once
}

func NewPortListener(connCh chan *PortConn, addr net.Addr) *PortListener {
	return &PortListener{
		connCh: connCh,
		addr:   addr,
		done:   make(chan struct{}),
	}
}

func (pListener *PortListener) Accept() (net.Conn, error) {
	// Prefer a closed listener when both the shutdown signal and a producer
	// are ready. This keeps Close from handing out a connection after it has
	// already unblocked a pending Accept.
	select {
	case <-pListener.done:
		return nil, net.ErrClosed
	default:
	}
	select {
	case <-pListener.done:
		return nil, net.ErrClosed
	case conn, ok := <-pListener.connCh:
		if !ok {
			return nil, net.ErrClosed
		}
		if conn != nil {
			select {
			case <-pListener.done:
				_ = conn.Close()
				return nil, net.ErrClosed
			default:
			}
			return conn, nil
		}
		return nil, net.ErrClosed
	}
}

func (pListener *PortListener) Close() error {
	pListener.closeOnce.Do(func() { close(pListener.done) })
	return nil
}

func (pListener *PortListener) Addr() net.Addr {
	return pListener.addr
}
