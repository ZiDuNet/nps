package client

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/p2pproto"
	"ehang.io/nps/lib/version"
	"github.com/astaxie/beego/logs"
	"github.com/djylb/holepunch"
	"github.com/djylb/holepunch/portmapper"
	"github.com/djylb/holepunch/protocol"
)

// modernP2PControlTimeout bounds sending the request and the TLS handshake of a
// P2P control stream; modernP2PReplyTimeout the wait for the bridge's reply, which
// a visitor gets once the bridge has sent the provider its invitation, in up
// to 10 s.
const (
	modernP2PControlTimeout = 10 * time.Second
	modernP2PReplyTimeout   = 15 * time.Second
)

// errModernP2PServerIncompatible is a P2P request the server closed or answered
// without a reply of this protocol: an nps server older than
// version.ModernP2PMinVersion.
var errModernP2PServerIncompatible = errors.New("the nps server does not speak this P2P protocol (it needs nps " + version.ModernP2PMinVersion + " or newer)")

// openModernP2PControl turns a fresh bridge stream into a P2P control stream: it
// sends the work type and msg, reads the bridge's reply and runs a TLS
// handshake that accepts only the bridge certificate with fingerprint, the
// one the bridge sent, encrypted with the vkey, when the tunnel was set up.
// It returns the TLS stream and the session id; on error c is closed.
// Canceling ctx closes c.
func openModernP2PControl(ctx context.Context, c *conn.Conn, _workType, _uuid string, expectedFingerprint []byte, msg any) (*tls.Conn, string, error) {
	if c == nil || c.Conn == nil {
		return nil, "", errors.New("P2P 控制连接为空")
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	_ = c.SetDeadline(time.Now().Add(modernP2PControlTimeout))
	if err := p2pproto.Write(c, msg); err != nil {
		_ = c.Close()
		return nil, "", fmt.Errorf("send request: %w", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(modernP2PReplyTimeout))
	var reply p2pproto.Reply
	if err := p2pproto.Read(c, &reply); err != nil {
		_ = c.Close()
		// An answer that is not a reply comes from a server of another
		// protocol; an ended stream may be a restart or a network change.
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) || errors.Is(err, p2pproto.ErrMessageSize) {
			return nil, "", fmt.Errorf("%w: %v", errModernP2PServerIncompatible, err)
		}
		return nil, "", fmt.Errorf("read reply: %w", err)
	}
	_ = c.SetDeadline(time.Now().Add(modernP2PControlTimeout))
	if err := reply.Err(); err != nil {
		_ = c.Close()
		return nil, "", err
	}
	fingerprint, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(reply.Fingerprint), "sha256:"))
	if err != nil || len(fingerprint) != 32 {
		_ = c.Close()
		return nil, "", errors.New("服务端未提供有效的 P2P TLS 证书指纹")
	}
	if len(expectedFingerprint) == 32 {
		if subtle.ConstantTimeCompare(fingerprint, expectedFingerprint) != 1 {
			_ = c.Close()
			return nil, "", errors.New("P2P 会话证书与已认证 Bridge 证书不一致")
		}
		fingerprint = expectedFingerprint
	}
	tc := tls.Client(c, crypt.NewPinnedTlsClientConfig(fingerprint))
	if err := tc.HandshakeContext(ctx); err != nil {
		_ = tc.Close()
		return nil, "", fmt.Errorf("control tls: %w", err)
	}
	_ = c.SetDeadline(time.Time{})
	return tc, reply.SessionID, nil
}

// modernP2POptions are the holepunch options of both sides: this side's pin as
// transport data, the local address of local_ip, the port mapper and
// whether this side accepts a background session.
func modernP2POptions(localIP string, mapper *portmapper.Mapper, background bool) holepunch.Options {
	opts := holepunch.Options{
		LocalAddr:        modernP2PLocalAddr(localIP),
		TransportMode:    p2pproto.TransportMode,
		TransportData:    modernP2PLocalPin(),
		AcceptBackground: background,
		OnProgress: func(p protocol.Progress) {
			logs.Trace("P2P session %s %s: %s %s %s", p.SessionID, p.Role, p.Stage, p.Status, p.Detail)
		},
	}
	if mapper != nil {
		opts.PortMapper = mapper
	}
	return opts
}

// modernP2PLocalAddr parses local_ip; anything that is not a unicast IP binds every
// address.
func modernP2PLocalAddr(localIP string) netip.Addr {
	a, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(localIP), "[]"))
	if err != nil || a.IsUnspecified() || a.IsMulticast() {
		return netip.Addr{}
	}
	return a.Unmap()
}

// modernP2PResultSummary describes a punched path for the log.
func modernP2PResultSummary(sess *holepunch.Session) string {
	res := sess.Result()
	p := res.Path
	return fmt.Sprintf("%s path %s -> %s over %s, rtt %s, keepalive %s (%s), %d rounds",
		p.Kind, p.LocalAddr, p.RemoteAddr, p.Family, p.RTT, p.Keepalive.Interval, p.Keepalive.Reason, res.Rounds)
}

// modernP2PNATSummary describes this side's NAT as the session measured it.
func modernP2PNATSummary(d *holepunch.Diagnostics) string {
	if d == nil {
		return "unknown"
	}
	var parts []string
	for _, t := range d.Self {
		parts = append(parts, fmt.Sprintf("%s mapping=%s filtering=%s ports=%s", t.Family, t.Mapping, t.Filtering, t.PortAlloc))
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, "; ")
}
