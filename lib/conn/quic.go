package conn

import (
	"context"
	"crypto/tls"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

// QuicStreamConn adapts one bidirectional QUIC stream to net.Conn. The NPS
// Bridge protocol is stream-oriented, so one stream carries the same framed
// handshake and mux bytes as a TCP connection.
type QuicStreamConn struct {
	stream *quic.Stream
	sess   *quic.Conn
}

func NewQuicStreamConn(stream *quic.Stream, sess *quic.Conn) *QuicStreamConn {
	return &QuicStreamConn{stream: stream, sess: sess}
}

func (c *QuicStreamConn) Read(p []byte) (int, error)    { return c.stream.Read(p) }
func (c *QuicStreamConn) Write(p []byte) (int, error)   { return c.stream.Write(p) }
func (c *QuicStreamConn) LocalAddr() net.Addr           { return c.sess.LocalAddr() }
func (c *QuicStreamConn) RemoteAddr() net.Addr          { return c.sess.RemoteAddr() }
func (c *QuicStreamConn) SetDeadline(t time.Time) error { return c.stream.SetDeadline(t) }
func (c *QuicStreamConn) SetReadDeadline(t time.Time) error {
	return c.stream.SetReadDeadline(t)
}
func (c *QuicStreamConn) SetWriteDeadline(t time.Time) error {
	return c.stream.SetWriteDeadline(t)
}
func (c *QuicStreamConn) Close() error { return c.stream.Close() }

// QuicAutoCloseConn closes the stream first and drains briefly before closing
// the session. This lets a final framed response reach the peer instead of
// being discarded by an immediate QUIC CONNECTION_CLOSE.
type QuicAutoCloseConn struct {
	*QuicStreamConn
	closeOnce sync.Once
}

func NewQuicAutoCloseConn(stream *quic.Stream, sess *quic.Conn) *QuicAutoCloseConn {
	return &QuicAutoCloseConn{QuicStreamConn: NewQuicStreamConn(stream, sess)}
}

func (c *QuicAutoCloseConn) Close() error {
	c.closeOnce.Do(func() {
		_ = c.QuicStreamConn.Close()
		go func() {
			timer := time.NewTimer(300 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-c.sess.Context().Done():
			case <-timer.C:
			}
			_ = c.sess.CloseWithError(0, "close")
		}()
	})
	return nil
}

// DialQuic opens a QUIC session and one stream for the Bridge protocol.
func DialQuic(ctx context.Context, server string, tlsConfig *tls.Config, quicConfig *quic.Config) (net.Conn, error) {
	sess, err := quic.DialAddr(ctx, server, tlsConfig, quicConfig)
	if err != nil {
		return nil, err
	}
	stream, err := sess.OpenStreamSync(ctx)
	if err != nil {
		_ = sess.CloseWithError(0, "open bridge stream")
		return nil, err
	}
	return NewQuicAutoCloseConn(stream, sess), nil
}

// ServeQuic accepts sessions and invokes f for the first bidirectional stream.
// A session without a stream within the handshake timeout is closed so UDP
// scans cannot accumulate indefinitely.
func ServeQuic(ctx context.Context, listener *quic.Listener, handshakeTimeout time.Duration, f func(net.Conn)) error {
	for {
		sess, err := listener.Accept(ctx)
		if err != nil {
			return err
		}
		go func() {
			streamCtx, cancel := context.WithTimeout(sess.Context(), handshakeTimeout)
			stream, streamErr := sess.AcceptStream(streamCtx)
			cancel()
			if streamErr != nil {
				_ = sess.CloseWithError(0, "bridge stream timeout")
				return
			}
			f(NewQuicAutoCloseConn(stream, sess))
		}()
	}
}

var _ net.Conn = (*QuicStreamConn)(nil)
var _ net.Conn = (*QuicAutoCloseConn)(nil)
