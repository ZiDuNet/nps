package client

import (
	"errors"
	"net"
	"time"

	"ehang.io/nps/lib/conn"
	mux "ehang.io/nps/lib/nps_mux"
	"ehang.io/nps/lib/p2pproto"
	"github.com/astaxie/beego/logs"
	"github.com/djylb/holepunch"
)

// serveModernP2P answers an invitation as the provider: it joins the session on a
// new stream of the tunnel, punches, accepts the visitor's QUIC connection
// and serves its streams like the tunnel's until the connection ends or the
// client closes.
func (s *TRPClient) serveModernP2P(tunnel any, invite p2pproto.Invite) {
	stream, err := s.openModernTunnelStream(tunnel)
	if err != nil {
		logs.Warn("P2P session %s: cannot open a stream to the server: %v", invite.SessionID, err)
		return
	}
	manager := s.p2pManager
	if manager == nil {
		logs.Warn("P2P session %s: modern manager is not initialized", invite.SessionID)
		_ = stream.Close()
		return
	}
	join := p2pproto.Join{SessionID: invite.SessionID, Token: invite.Token}
	if DisableP2P {
		logs.Info("P2P session %s declined: P2P is disabled on this client", invite.SessionID)
		join.Decline = "P2P is disabled on the provider"
		c := conn.NewConn(stream)
		if err := writeModernP2PWorkType(c, "p2sj"); err == nil {
			_ = c.SetWriteDeadline(time.Now().Add(modernP2PControlTimeout))
			_ = p2pproto.Write(c, join)
			_ = c.Close()
		}
		return
	}
	if err := writeModernP2PWorkType(conn.NewConn(stream), "p2sj"); err != nil {
		_ = stream.Close()
		logs.Warn("P2P session %s: cannot identify join stream: %v", invite.SessionID, err)
		return
	}
	ctx := manager.ctx
	tc, _, err := openModernP2PControl(ctx, conn.NewConn(stream), "", "", nil, join)
	if err != nil {
		logs.Warn("P2P session %s: join failed: %v", invite.SessionID, err)
		return
	}
	// The provider accepts a background session whenever the server offers
	// one: the visitor relays meanwhile.
	sess, err := holepunch.Punch(ctx, tc, modernP2POptions(manager.cfg.LocalIP, manager.mapper, true))
	_ = tc.Close()
	if err != nil {
		var herr *holepunch.Error
		if errors.As(err, &herr) {
			logs.Warn("P2P session %s: no direct path: %v; local NAT %s", invite.SessionID, err, modernP2PNATSummary(herr.Diagnostics))
		} else {
			logs.Warn("P2P session %s: no direct path: %v", invite.SessionID, err)
		}
		return
	}
	diag := sess.Diagnostics()
	logs.Info("P2P session %s: punched %s; local NAT %s", invite.SessionID, modernP2PResultSummary(sess), modernP2PNATSummary(&diag))
	t, err := acceptModernP2PTransport(ctx, sess)
	if err != nil {
		logs.Warn("P2P session %s: visitor did not connect over the direct path: %v", invite.SessionID, err)
		return
	}
	defer t.Close(modernP2PCodeClosed, "closed")
	logs.Info("P2P session %s: direct QUIC connection from %v established", invite.SessionID, t.qc.RemoteAddr())
	for {
		st, err := t.qc.AcceptStream(ctx)
		if err != nil {
			logs.Info("P2P session %s: direct connection from %v closed: %v", invite.SessionID, t.qc.RemoteAddr(), err)
			return
		}
		go s.handleChan(conn.NewQuicStreamConn(st, t.qc))
	}
}

func writeModernP2PWorkType(c *conn.Conn, workType string) error {
	if c == nil || c.Conn == nil {
		return errors.New("P2P 控制连接为空")
	}
	_, err := c.Write([]byte(workType))
	return err
}

// openModernTunnelStream opens a stream to the bridge on the client's tunnel.
func (s *TRPClient) openModernTunnelStream(tunnel any) (net.Conn, error) {
	switch t := tunnel.(type) {
	case *mux.Mux:
		return t.NewConn()
	}
	return nil, errors.New("no tunnel to the server")
}
