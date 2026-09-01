package pmux

import (
	"net"
	"sync"
	"time"
)

type PortConn struct {
	Conn     net.Conn
	rs       []byte
	readMore bool
	start    int
	readMu   sync.Mutex
}

func newPortConn(conn net.Conn, rs []byte, readMore bool) *PortConn {
	return &PortConn{
		Conn:     conn,
		rs:       rs,
		readMore: readMore,
	}
}

func (pConn *PortConn) Read(b []byte) (n int, err error) {
	pConn.readMu.Lock()
	defer pConn.readMu.Unlock()
	if pConn.start < len(pConn.rs) {
		n = copy(b, pConn.rs[pConn.start:])
		pConn.start += n
		if n == len(b) || !pConn.readMore {
			return
		}
	}
	var n2 int
	n2, err = pConn.Conn.Read(b[n:])
	n = n + n2
	return
}

func (pConn *PortConn) Write(b []byte) (n int, err error) {
	return pConn.Conn.Write(b)
}

func (pConn *PortConn) Close() error {
	return pConn.Conn.Close()
}

func (pConn *PortConn) LocalAddr() net.Addr {
	return pConn.Conn.LocalAddr()
}

func (pConn *PortConn) RemoteAddr() net.Addr {
	return pConn.Conn.RemoteAddr()
}

func (pConn *PortConn) SetDeadline(t time.Time) error {
	return pConn.Conn.SetDeadline(t)
}

func (pConn *PortConn) SetReadDeadline(t time.Time) error {
	return pConn.Conn.SetReadDeadline(t)
}

func (pConn *PortConn) SetWriteDeadline(t time.Time) error {
	return pConn.Conn.SetWriteDeadline(t)
}
