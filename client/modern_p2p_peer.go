package client

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/p2pproto"
	"github.com/astaxie/beego/logs"
	"github.com/djylb/holepunch"
)

// modernP2PStableAfter is how long a direct path must have lived for its loss to
// be retried at once; a path lost sooner counts as a failed attempt.
const modernP2PStableAfter = time.Minute

var errModernP2PPathLost = errors.New("direct path lost soon after it was established")

// modernP2PPeer is the direct path of the visitor to one P2P task (one password):
// every local section with that password shares it. Its goroutine punches,
// holds the QUIC transport while it lives and punches again with a backoff
// when it ends.
type modernP2PPeer struct {
	mgr      *P2PManager
	password string
	label    string
	relay    atomic.Bool // some section relays when the direct path is down

	mu        sync.Mutex
	transport *modernP2PTransport
	changed   chan struct{} // closed and replaced whenever a transport is installed
}

func newModernP2PPeer(mgr *P2PManager, password, label string) *modernP2PPeer {
	return &modernP2PPeer{mgr: mgr, password: password, label: label, changed: make(chan struct{})}
}

// active returns the live direct transport that is not under suspicion, or
// nil.
func (p *modernP2PPeer) active() *modernP2PTransport {
	p.mu.Lock()
	t := p.transport
	p.mu.Unlock()
	if t != nil && t.Alive() && !t.suspect.Load() {
		return t
	}
	return nil
}

// waitActive waits for a live direct transport until ctx ends.
func (p *modernP2PPeer) waitActive(ctx context.Context) *modernP2PTransport {
	for {
		p.mu.Lock()
		t, changed := p.transport, p.changed
		p.mu.Unlock()
		if t != nil && t.Alive() && !t.suspect.Load() {
			return t
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil
		}
	}
}

func (p *modernP2PPeer) install(t *modernP2PTransport) {
	p.mu.Lock()
	p.transport = t
	close(p.changed)
	p.changed = make(chan struct{})
	p.mu.Unlock()
}

func (p *modernP2PPeer) uninstall(t *modernP2PTransport) {
	p.mu.Lock()
	if p.transport == t {
		p.transport = nil
	}
	p.mu.Unlock()
}

// suspect takes a transport that failed a connection out of use for new
// connections and checks it: a slow moment must not cost the streams it
// carries, a dead path is closed. A transport that passes the check is used
// again.
func (p *modernP2PPeer) suspect(t *modernP2PTransport, err error) {
	if p.mgr.ctx.Err() != nil || !t.suspect.CompareAndSwap(false, true) {
		return
	}
	logs.Info("%s: direct path to %v failed a connection (%v), checking it", p.label, t.qc.RemoteAddr(), err)
	p.mgr.wg.Go(func() {
		var last error
		for range modernP2PCheckAttempts {
			if last = t.check(p.mgr.ctx); last == nil {
				t.suspect.Store(false)
				p.mu.Lock()
				close(p.changed)
				p.changed = make(chan struct{})
				p.mu.Unlock()
				logs.Info("%s: direct path to %v answers again", p.label, t.qc.RemoteAddr())
				return
			}
			if !t.Alive() || p.mgr.ctx.Err() != nil {
				return
			}
		}
		p.lost(t, last)
	})
}

// lost takes a dead transport out of use and closes it, which wakes the
// goroutine holding it to punch again.
func (p *modernP2PPeer) lost(t *modernP2PTransport, err error) {
	logs.Warn("%s: direct path to %v failed: %v", p.label, t.qc.RemoteAddr(), err)
	p.uninstall(t)
	t.Close(modernP2PCodeLost, "lost")
}

// hold serves t until its QUIC connection ends or the manager closes, and
// returns how long it lived.
func (p *modernP2PPeer) hold(t *modernP2PTransport) time.Duration {
	start := time.Now()
	p.install(t)
	select {
	case <-t.Done():
		logs.Warn("%s: direct path to %v closed: %v", p.label, t.qc.RemoteAddr(), context.Cause(t.qc.Context()))
	case <-p.mgr.ctx.Done():
	}
	p.uninstall(t)
	t.Close(modernP2PCodeClosed, "closed")
	return time.Since(start)
}

// run punches and holds direct paths until the manager closes.
func (p *modernP2PPeer) run() {
	ctx := p.mgr.ctx
	var b modernP2PBackoff
	var wait time.Duration
	background := false
	for {
		if !modernSleepWithContext(ctx, wait) {
			return
		}
		t, err := p.punch(ctx, background)
		if ctx.Err() != nil {
			if t != nil {
				t.Close(modernP2PCodeClosed, "closed")
			}
			return
		}
		if err != nil {
			wait, background = b.failed(err, p.relay.Load(), background)
			logs.Warn("%s: no direct path: %v; next %s attempt in %s", p.label, err, modernP2PModeName(background), wait.Round(time.Second))
			continue
		}
		background = false
		if p.hold(t) >= modernP2PStableAfter {
			b.reset()
			wait = 0
		} else {
			wait, _ = b.failed(errModernP2PPathLost, false, false)
		}
	}
}

// punch runs one hole-punching session through the bridge and connects QUIC
// over the punched path.
func (p *modernP2PPeer) punch(ctx context.Context, background bool) (*modernP2PTransport, error) {
	mgr := p.mgr
	rc, server, err := mgr.openTunnelConn()
	if err != nil {
		return nil, err
	}
	// openModernP2PControl owns the stream and closes it on failure.
	//noinspection GoResourceLeak
	tc, sessionID, err := openModernP2PControl(ctx, conn.NewConn(rc), "", "", server.fingerprint,
		p2pproto.Request{Password: crypt.Md5(p.password), Background: background, ServerIP: server.ip, NodeUUID: mgr.getUUID()})
	if err != nil {
		return nil, err
	}
	logs.Debug("%s: punching in session %s (%s)", p.label, sessionID, modernP2PModeName(background))
	// tc is closed right below; on success the session is handed to the transport.
	//noinspection GoResourceLeak
	sess, err := holepunch.Punch(ctx, tc, modernP2POptions(mgr.cfg.LocalIP, mgr.mapper, background))
	_ = tc.Close()
	if err != nil {
		var herr *holepunch.Error
		if errors.As(err, &herr) {
			logs.Debug("%s: session %s failed, local NAT %s", p.label, sessionID, modernP2PNATSummary(herr.Diagnostics))
		}
		return nil, err
	}
	diag := sess.Diagnostics()
	logs.Info("%s: punched %s; local NAT %s", p.label, modernP2PResultSummary(sess), modernP2PNATSummary(&diag))
	t, err := dialModernP2PTransport(ctx, sess)
	if err != nil {
		return nil, err
	}
	logs.Info("%s: direct QUIC connection to %v established", p.label, t.qc.RemoteAddr())
	return t, nil
}

func modernP2PModeName(background bool) string {
	if background {
		return "background"
	}
	return "interactive"
}
