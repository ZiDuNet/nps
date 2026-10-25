package conn

import (
	"io"
	"net"
	"time"

	"ehang.io/nps/lib/common"
)

// WriteACK and ReadACK are used by the optional Secret Link protocol. They
// are deliberately tiny and additive so old Secret connections, which never
// request an acknowledgement, continue to use the historical raw stream.
func WriteACK(c net.Conn, timeout time.Duration) error {
	if c == nil {
		return io.ErrClosedPipe
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	_ = c.SetWriteDeadline(time.Now().Add(timeout))
	_, err := io.WriteString(c, common.CONN_ACK)
	_ = c.SetWriteDeadline(time.Time{})
	return err
}

func ReadACK(c net.Conn, timeout time.Duration) error {
	if c == nil {
		return io.ErrClosedPipe
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, len(common.CONN_ACK))
	_, err := io.ReadFull(c, buf)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		return err
	}
	if string(buf) != common.CONN_ACK {
		return io.ErrUnexpectedEOF
	}
	return nil
}
