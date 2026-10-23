package rate

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"sync/atomic"
	"time"

	xrate "golang.org/x/time/rate"
)

type Rate struct {
	limiter    *xrate.Limiter
	addSize    int64
	burst      int
	parent     *Rate
	mu         sync.RWMutex
	stopChan   chan struct{}
	stopOnce   sync.Once
	startOnce  sync.Once
	ctx        context.Context
	cancel     context.CancelFunc
	consumed   int64
	NowRate    int64
	lazySample bool
	sampleMu   sync.Mutex
	sampleAt   time.Time
}

var globalRate = newRate(0, nil)

func newRate(addSize int64, parent *Rate) *Rate {
	if addSize < 0 {
		addSize = 0
	}
	burst := int64(math.MaxInt)
	if int64(int(burst)) != burst {
		burst = int64(^uint(0) >> 1)
	}
	if addSize < burst {
		burst = addSize
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Rate{
		limiter:  xrate.NewLimiter(xrate.Limit(addSize), int(burst)),
		addSize:  addSize,
		burst:    int(burst),
		parent:   parent,
		stopChan: make(chan struct{}),
		ctx:      ctx,
		cancel:   cancel,
	}
}

func NewRate(addSize int64) *Rate {
	// Every ordinary rate is automatically chained to the server-wide rate.
	// This keeps the existing client limiter API intact while making the
	// aggregate limit apply to all forwarding paths, including connections that
	// were already open when the global setting changed.
	return newRate(addSize, globalRate)
}

// NewStandaloneRate creates a limiter that is not attached to the global
// server bucket. It is used by resource-scoped directional limiters, which
// are layered on top of the existing client/global limiter and therefore must
// not charge the global bucket a second time.
func NewStandaloneRate(addSize int64) *Rate {
	r := newRate(addSize, nil)
	r.lazySample = true
	r.sampleAt = time.Now()
	return r
}

// ResetLimit updates a limiter in place. Existing connections observe the new
// value without having to be rebuilt.
func (s *Rate) ResetLimit(limit int64) {
	s.setLimit(limit)
}

// Limit returns the configured byte-per-second limit. A non-positive value is
// unlimited.
func (s *Rate) Limit() int64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	limit := s.addSize
	s.mu.RUnlock()
	return limit
}

// SetGlobalLimit changes the aggregate forwarding limit in bytes per second.
// A non-positive value disables the limit. The shared limiter is intentionally
// kept alive so existing connections observe the new value immediately.
func SetGlobalLimit(limit int64) {
	if limit < 0 {
		limit = 0
	}
	globalRate.setLimit(limit)
	if limit > 0 {
		globalRate.Start()
	}
}

// GlobalLimit reports the aggregate forwarding limit in bytes per second.
func GlobalLimit() int64 {
	if globalRate == nil {
		return 0
	}
	globalRate.mu.RLock()
	limit := globalRate.addSize
	globalRate.mu.RUnlock()
	return limit
}

func (s *Rate) setLimit(limit int64) {
	if s == nil {
		return
	}
	if limit < 0 {
		limit = 0
	}
	burst := int64(math.MaxInt)
	if int64(int(burst)) != burst {
		burst = int64(^uint(0) >> 1)
	}
	if limit < burst {
		burst = limit
	}
	s.mu.Lock()
	s.addSize = limit
	s.burst = int(burst)
	if s.limiter != nil {
		now := time.Now()
		s.limiter.SetLimitAt(now, xrate.Limit(limit))
		s.limiter.SetBurstAt(now, int(burst))
	}
	s.mu.Unlock()
}

func (s *Rate) Start() {
	if s == nil || s.stopChan == nil {
		return
	}
	s.startOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					atomic.StoreInt64(&s.NowRate, atomic.SwapInt64(&s.consumed, 0))
				case <-s.stopChan:
					return
				}
			}
		}()
	})
}

func (s *Rate) Stop() {
	if s == nil || s.stopChan == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopChan)
		if s.cancel != nil {
			s.cancel()
		}
	})
}

func (s *Rate) Get(size int64) {
	if s == nil || size <= 0 {
		return
	}
	// Keep throughput accounting independent of whether a limit is configured.
	// This is used by the directional resource metrics as well as the legacy
	// client-rate display.
	atomic.AddInt64(&s.consumed, size)
	if s.lazySample {
		s.sampleLazyRate()
	}
	s.mu.RLock()
	limiter, addSize, burst, ctx, parent := s.limiter, s.addSize, s.burst, s.ctx, s.parent
	s.mu.RUnlock()
	if addSize > 0 && limiter != nil && ctx != nil && burst > 0 {
		for remaining := size; remaining > 0; {
			chunk := remaining
			if chunk > int64(burst) {
				chunk = int64(burst)
			}
			if err := limiter.WaitN(ctx, int(chunk)); err != nil {
				break
			}
			remaining -= chunk
		}
	}
	if parent != nil {
		parent.Get(size)
	}
}

// CurrentRate returns the last complete one-second transfer sample.
func (s *Rate) CurrentRate() int64 {
	if s == nil {
		return 0
	}
	if s.lazySample {
		s.sampleLazyRate()
		s.sampleMu.Lock()
		stale := time.Since(s.sampleAt) > 2*time.Second
		s.sampleMu.Unlock()
		if stale {
			return 0
		}
	}
	return atomic.LoadInt64(&s.NowRate)
}

func (s *Rate) sampleLazyRate() {
	now := time.Now()
	s.sampleMu.Lock()
	defer s.sampleMu.Unlock()
	if s.sampleAt.IsZero() {
		s.sampleAt = now
		return
	}
	elapsed := now.Sub(s.sampleAt)
	if elapsed < time.Second {
		return
	}
	bytes := atomic.SwapInt64(&s.consumed, 0)
	atomic.StoreInt64(&s.NowRate, int64(float64(bytes)/elapsed.Seconds()))
	s.sampleAt = now
}

// MarshalJSON keeps status responses race-free while retaining the historical
// field name consumed by the web console.
func (s *Rate) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		NowRate int64 `json:"NowRate"`
	}{NowRate: s.CurrentRate()})
}
