package client

import (
	"errors"
	"math/rand/v2"
	"time"

	"ehang.io/nps/lib/p2pproto"
	"github.com/djylb/holepunch"
	"github.com/djylb/holepunch/protocol"
)

// modernP2PBackoffClass groups P2P failures by what retrying them achieves.
type modernP2PBackoffClass uint8

const (
	// modernP2PBackoffTransient: the peer, the server or the control link failed
	// for now (holepunch ClassTransient, a canceled session whose control
	// link went away, a busy server, an offline provider, I/O errors); retry
	// soon.
	modernP2PBackoffTransient modernP2PBackoffClass = iota
	// modernP2PBackoffNATHard: the NAT pair did not connect this time (holepunch
	// ClassNATHard); the server's strategy memory tries another plan later.
	modernP2PBackoffNATHard
	// modernP2PBackoffRelayOnly: the server expects no mode to connect this pair
	// (advice relay_only); stay on the relay for long.
	modernP2PBackoffRelayOnly
	// modernP2PBackoffFatal: configuration, version or local network problems
	// (holepunch ClassFatal, a refused task); retrying soon ends the same
	// way.
	modernP2PBackoffFatal
)

// modernP2PSchedule is a backoff: base doubling per consecutive failure of the
// same class up to max, plus a uniform jitter in [0, base) when set.
type modernP2PSchedule struct {
	base, max time.Duration
	jitter    bool
}

var modernP2PSchedules = [...]modernP2PSchedule{
	modernP2PBackoffTransient: {base: 5 * time.Second, max: time.Minute, jitter: true},
	modernP2PBackoffNATHard:   {base: 30 * time.Second, max: 10 * time.Minute},
	modernP2PBackoffRelayOnly: {base: 2 * time.Minute, max: 30 * time.Minute},
	modernP2PBackoffFatal:     {base: 10 * time.Minute, max: 10 * time.Minute},
}

// classifyModernP2PError returns the backoff class of a failed punch or transport
// setup, the server's retry_after and its advice.
func classifyModernP2PError(err error) (modernP2PBackoffClass, time.Duration, protocol.Advice) {
	var herr *holepunch.Error
	var refused *p2pproto.RefusedError
	switch {
	case errors.As(err, &herr):
		var class modernP2PBackoffClass
		switch herr.Reason.Class() {
		case protocol.ClassFatal:
			class = modernP2PBackoffFatal
		case protocol.ClassNATHard:
			class = modernP2PBackoffNATHard
		default:
			class = modernP2PBackoffTransient
		}
		if herr.Advice == protocol.AdviceRelayOnly {
			class = modernP2PBackoffRelayOnly
		}
		return class, herr.RetryAfter, herr.Advice
	case errors.Is(err, errModernP2PServerIncompatible):
		return modernP2PBackoffFatal, 0, ""
	case errors.As(err, &refused):
		switch refused.Code {
		case p2pproto.CodeOffline, p2pproto.CodeBusy:
			return modernP2PBackoffTransient, 0, ""
		}
		return modernP2PBackoffFatal, 0, ""
	}
	return modernP2PBackoffTransient, 0, ""
}

// modernP2PBackoff decides when and how a visitor punches again.
type modernP2PBackoff struct {
	class modernP2PBackoffClass
	fails int // consecutive failures of class
}

// failed records a failed attempt and returns the wait before the next one
// and whether the next one runs in background mode. relay says whether
// connections are relayed meanwhile; background whether the failed attempt
// ran in background mode. A failure advising background_viable while relaying
// is retried in background mode right away the first time. The caller
// does not record attempts its own closing canceled.
func (b *modernP2PBackoff) failed(err error, relay, background bool) (time.Duration, bool) {
	class, retryAfter, advice := classifyModernP2PError(err)
	if class != b.class {
		b.class, b.fails = class, 0
	}
	b.fails++
	nextBackground := relay && advice == protocol.AdviceBackgroundViable
	if nextBackground && !background {
		return 0, true
	}
	s := modernP2PSchedules[class]
	wait := s.base
	for i := 1; i < b.fails && wait < s.max; i++ {
		wait *= 2
	}
	wait = min(wait, s.max)
	if s.jitter {
		wait += rand.N(s.base)
	}
	return max(wait, retryAfter), nextBackground
}

// reset forgets the failures after a success.
func (b *modernP2PBackoff) reset() { *b = modernP2PBackoff{} }
