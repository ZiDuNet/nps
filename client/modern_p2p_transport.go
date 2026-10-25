package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/p2pproto"
	"github.com/djylb/holepunch"
	"github.com/quic-go/quic-go"
)

const (
	// modernP2POpenStreamTimeout bounds opening a stream on the direct path and
	// sending the link information over it; past it the connection falls
	// back to the relay when that is allowed.
	modernP2POpenStreamTimeout = 2 * time.Second
	// modernP2PIdleProbeAfter is how long after the provider was last heard from
	// a new stream asks it for an acknowledgement before it is used: a
	// silently broken path (an expired NAT mapping, a changed network)
	// accepts writes until QUIC's idle timeout, so only an answer proves it.
	modernP2PIdleProbeAfter = 5 * time.Second
	// modernP2PCheckTimeout bounds one liveness check of a suspect path,
	// modernP2PCheckAttempts how many it gets before it is closed.
	modernP2PCheckTimeout  = 2 * time.Second
	modernP2PCheckAttempts = 2
	// modernP2PDialAttempts bounds the QUIC handshake attempts within the
	// session's transport timeout, modernP2PDialRetryDelay separating them.
	modernP2PDialAttempts   = 3
	modernP2PDialRetryDelay = 200 * time.Millisecond
	// modernP2PDefaultTransportTimeout applies when the session carries none.
	modernP2PDefaultTransportTimeout = 10 * time.Second
	// modernP2PDefaultIdleTimeout applies when the path has no keepalive advice.
	modernP2PDefaultIdleTimeout = 30 * time.Second
)

// QUIC application error codes on the punched path.
const (
	modernP2PCodeClosed         quic.ApplicationErrorCode = 0
	modernP2PCodeUnexpectedPeer quic.ApplicationErrorCode = 1
	modernP2PCodeLost           quic.ApplicationErrorCode = 2
	// modernP2PStreamAbandoned resets a stream whose link was not delivered.
	modernP2PStreamAbandoned quic.StreamErrorCode = 1
)

// modernP2PALPN is the application protocol of the QUIC connection on the punched
// path: HTTP/3's, the most common one on UDP, so that the connection does not
// stand out (the bridge's own QUIC uses quic_alpn, nps by default). Both
// sides authenticate by their pinned certificates alone.
const modernP2PALPN = "h3"

var errModernP2PTransportMode = errors.New("p2p session did not settle on a pinned QUIC transport")

// modernP2PTransport is a QUIC connection on a punched path, together with the
// QUIC transport reading the socket and the release of the punched socket
// (the holepunch session's Close, which also drops a port mapping). Close
// releases all three; it follows the QUIC connection's end.
type modernP2PTransport struct {
	qc      *quic.Conn
	tr      *quic.Transport
	release func() error

	lastHeard atomic.Int64 // unix nanoseconds of the provider's last answer
	suspect   atomic.Bool  // a connection failed on it; a check is deciding
	closeOnce sync.Once
}

func newModernP2PTransport(qc *quic.Conn, tr *quic.Transport, release func() error) *modernP2PTransport {
	t := &modernP2PTransport{qc: qc, tr: tr, release: release}
	t.heard() // the handshake just completed
	return t
}

// Done is closed when the QUIC connection ends, for whatever reason.
func (t *modernP2PTransport) Done() <-chan struct{} { return t.qc.Context().Done() }

// Alive reports whether the QUIC connection is still up.
func (t *modernP2PTransport) Alive() bool { return t.qc.Context().Err() == nil }

// heard records an answer of the provider.
func (t *modernP2PTransport) heard() { t.lastHeard.Store(time.Now().UnixNano()) }

// silence is the time since the provider last answered.
func (t *modernP2PTransport) silence() time.Duration {
	return time.Since(time.Unix(0, t.lastHeard.Load()))
}

// Close closes the QUIC connection with code, stops the transport and
// releases the socket. It is idempotent.
func (t *modernP2PTransport) Close(code quic.ApplicationErrorCode, reason string) {
	t.closeOnce.Do(func() {
		_ = t.qc.CloseWithError(code, reason)
		_ = t.tr.Close()
		if t.release != nil {
			_ = t.release()
		}
	})
}

// openLink opens a stream and sends link over it; when the provider has not
// answered for modernP2PIdleProbeAfter it also waits for its acknowledgement. Each
// step is bounded by modernP2POpenStreamTimeout. On failure the stream is
// reset in both directions, so that the provider drops whatever part of the
// link it received, and no user data has been written to it.
func (t *modernP2PTransport) openLink(ctx context.Context, link conn.Link) (net.Conn, error) {
	link.Option.NeedAck = t.silence() > modernP2PIdleProbeAfter
	return t.open(ctx, link, modernP2POpenStreamTimeout)
}

// check asks the provider for an acknowledgement on a stream of its own: a
// link without a host, which the provider acknowledges and closes.
func (t *modernP2PTransport) check(ctx context.Context) error {
	c, err := t.open(ctx, conn.Link{Option: conn.Options{NeedAck: true, Timeout: modernP2PCheckTimeout}}, modernP2PCheckTimeout)
	if err == nil {
		_ = c.Close()
	}
	return err
}

func (t *modernP2PTransport) open(ctx context.Context, link conn.Link, timeout time.Duration) (net.Conn, error) {
	octx, cancel := context.WithTimeout(ctx, timeout)
	stream, err := t.qc.OpenStreamSync(octx)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	fail := func(err error) (net.Conn, error) {
		stream.CancelWrite(modernP2PStreamAbandoned)
		stream.CancelRead(modernP2PStreamAbandoned)
		return nil, err
	}
	nc := conn.NewQuicStreamConn(stream, t.qc)
	_ = stream.SetWriteDeadline(time.Now().Add(timeout))
	// The wrapper only writes to nc, whose stream fail cancels on failure.
	//noinspection GoResourceLeak
	if _, err := conn.NewConn(nc).SendInfo(&link, ""); err != nil {
		return fail(fmt.Errorf("send link: %w", err))
	}
	_ = stream.SetWriteDeadline(time.Time{})
	if link.Option.NeedAck {
		if err := conn.ReadACK(nc, timeout); err != nil {
			return fail(fmt.Errorf("read ack: %w", err))
		}
		t.heard()
	}
	return nc, nil
}

// modernP2PQUICConfig derives the QUIC configuration from the punched path: the
// keepalive and idle timeout follow the path's keepalive advice, so that a
// dead path is noticed as early as the NAT allows, and the handshake is
// bounded by the session's transport timeout.
func modernP2PQUICConfig(res holepunch.Result) *quic.Config {
	idle := res.Path.Keepalive.Timeout
	if idle <= 0 {
		idle = modernP2PDefaultIdleTimeout
	}
	keepalive := res.Path.Keepalive.Interval
	if keepalive <= 0 || keepalive >= idle {
		keepalive = idle / 3
	}
	return &quic.Config{
		HandshakeIdleTimeout: modernP2PTransportTimeout(res),
		MaxIdleTimeout:       idle,
		KeepAlivePeriod:      keepalive,
		MaxIncomingStreams:   1024,
	}
}

func modernP2PTransportTimeout(res holepunch.Result) time.Duration {
	if res.TransportTimeout > 0 {
		return res.TransportTimeout
	}
	return modernP2PDefaultTransportTimeout
}

// modernP2PLocalPin is this side's certificate fingerprint, sent to the peer as
// TransportData.
func modernP2PLocalPin() []byte {
	return crypt.GetCertificateFingerprint(crypt.GetCert())
}

// modernP2PServerTLS is the provider's QUIC TLS configuration: the visitor must
// present the certificate with pin, the fingerprint the visitor sent through
// the coordinator. No session tickets: every connection authenticates
// afresh.
func modernP2PServerTLS(pin []byte) *tls.Config {
	return crypt.NewPinnedTlsServerConfig(pin, crypt.GetCert(), modernP2PALPN)
}

// modernP2PClientTLS is the visitor's QUIC TLS configuration: the provider's
// fingerprint, sent through the coordinator, replaces the chain
// verification.
func modernP2PClientTLS(pin []byte) *tls.Config {
	cfg := crypt.NewPinnedTlsClientConfig(pin)
	cfg.Certificates = []tls.Certificate{crypt.GetCert()}
	cfg.ServerName = "nps-p2p.local"
	cfg.NextProtos = []string{modernP2PALPN}
	return cfg
}

// modernP2PSocket checks that sess settled on a pinned QUIC transport and returns
// the peer's pin and the punched socket, whose deadlines the session has
// cleared.
func modernP2PSocket(sess *holepunch.Session) (holepunch.Result, []byte, net.PacketConn, error) {
	res := sess.Result()
	if res.TransportMode != p2pproto.TransportMode || len(res.PeerTransportData) == 0 {
		return res, nil, nil, fmt.Errorf("%w: mode %q", errModernP2PTransportMode, res.TransportMode)
	}
	return res, res.PeerTransportData, sess.PacketConn(), nil
}

// dialModernP2PTransport connects QUIC to the provider over the punched path, in
// at most modernP2PDialAttempts within the session's transport timeout. It takes
// sess over: on failure sess is closed.
func dialModernP2PTransport(ctx context.Context, sess *holepunch.Session) (*modernP2PTransport, error) {
	res, pin, pc, err := modernP2PSocket(sess)
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	tr := &quic.Transport{Conn: pc}
	dctx, cancel := context.WithTimeout(ctx, modernP2PTransportTimeout(res))
	defer cancel()
	remote := net.UDPAddrFromAddrPort(res.Path.RemoteAddr)
	tlsCfg, qcfg := modernP2PClientTLS(pin), modernP2PQUICConfig(res)
	var last error
	for attempt := 1; ; attempt++ {
		qc, err := tr.Dial(dctx, remote, tlsCfg, qcfg)
		if err == nil {
			return newModernP2PTransport(qc, tr, sess.Close), nil
		}
		last = err
		if attempt >= modernP2PDialAttempts || dctx.Err() != nil || modernP2PDialFinal(err) || !modernSleepWithContext(dctx, modernP2PDialRetryDelay) {
			break
		}
	}
	_ = tr.Close()
	_ = sess.Close()
	return nil, fmt.Errorf("quic dial %v: %w", res.Path.RemoteAddr, last)
}

// acceptModernP2PTransport waits, within the session's transport timeout, for the
// visitor's QUIC connection from the punched address with the visitor's pin.
// It takes sess over: on failure sess is closed.
func acceptModernP2PTransport(ctx context.Context, sess *holepunch.Session) (*modernP2PTransport, error) {
	res, pin, pc, err := modernP2PSocket(sess)
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	tr := &quic.Transport{Conn: pc}
	ln, err := tr.Listen(modernP2PServerTLS(pin), modernP2PQUICConfig(res))
	if err != nil {
		_ = tr.Close()
		_ = sess.Close()
		return nil, fmt.Errorf("quic listen: %w", err)
	}
	defer func() { _ = ln.Close() }()
	actx, cancel := context.WithTimeout(ctx, modernP2PTransportTimeout(res))
	defer cancel()
	for {
		qc, err := ln.Accept(actx)
		if err != nil {
			_ = tr.Close()
			_ = sess.Close()
			return nil, fmt.Errorf("quic accept: %w", err)
		}
		if sameModernP2PAddrPort(qc.RemoteAddr(), res.Path.RemoteAddr) {
			return newModernP2PTransport(qc, tr, sess.Close), nil
		}
		_ = qc.CloseWithError(modernP2PCodeUnexpectedPeer, "unexpected peer")
	}
}

// modernP2PDialFinal reports a dial error another attempt cannot cure: a closed
// socket or transport, or a TLS failure such as a pin mismatch.
func modernP2PDialFinal(err error) bool {
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	var te *quic.TransportError
	return errors.As(err, &te) && te.ErrorCode.IsCryptoError()
}

// sameModernP2PAddrPort compares a socket address with the punched peer address.
func sameModernP2PAddrPort(a net.Addr, want netip.AddrPort) bool {
	ua, ok := a.(*net.UDPAddr)
	if !ok {
		return false
	}
	got := ua.AddrPort()
	return got.Addr().Unmap() == want.Addr().Unmap() && got.Port() == want.Port()
}
