package conn

import (
	"bytes"
	"net"
	"sync"
)

// TeeConn captures a small prefix of a stream while it is probed for an
// optional framed Link header. If the probe is not a Link (legacy Secret
// traffic), Buffered returns the bytes so forwarding can replay them without
// losing the first part of an HTTP request or arbitrary TCP payload.
type TeeConn struct {
	net.Conn
	mu      sync.Mutex
	buf     bytes.Buffer
	max     int
	capture bool
}

func NewTeeConn(conn net.Conn) *TeeConn {
	return &TeeConn{Conn: conn, max: 64 << 10, capture: true}
}

func (c *TeeConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.mu.Lock()
		if c.capture && c.buf.Len() < c.max {
			remaining := c.max - c.buf.Len()
			captured := n
			if captured > remaining {
				captured = remaining
			}
			_, _ = c.buf.Write(p[:captured])
		}
		c.mu.Unlock()
	}
	return n, err
}

func (c *TeeConn) Buffered() []byte {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.buf.Bytes()...)
}

func (c *TeeConn) StopAndClean() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.capture = false
	c.mu.Unlock()
}

func (c *TeeConn) ClearBuffered() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.buf.Reset()
	c.mu.Unlock()
}
