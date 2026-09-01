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
	limiter   *xrate.Limiter
	addSize   int64
	burst     int
	stopChan  chan struct{}
	stopOnce  sync.Once
	startOnce sync.Once
	ctx       context.Context
	cancel    context.CancelFunc
	consumed  int64
	NowRate   int64
}

func NewRate(addSize int64) *Rate {
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
		stopChan: make(chan struct{}),
		ctx:      ctx,
		cancel:   cancel,
	}
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
	if s == nil || s.addSize <= 0 || s.limiter == nil || s.ctx == nil || s.burst <= 0 {
		return
	}
	for remaining := size; remaining > 0; {
		chunk := remaining
		if chunk > int64(s.burst) {
			chunk = int64(s.burst)
		}
		if err := s.limiter.WaitN(s.ctx, int(chunk)); err != nil {
			return
		}
		atomic.AddInt64(&s.consumed, chunk)
		remaining -= chunk
	}
}

// CurrentRate returns the last complete one-second transfer sample.
func (s *Rate) CurrentRate() int64 {
	if s == nil {
		return 0
	}
	return atomic.LoadInt64(&s.NowRate)
}

// MarshalJSON keeps status responses race-free while retaining the historical
// field name consumed by the web console.
func (s *Rate) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		NowRate int64 `json:"NowRate"`
	}{NowRate: s.CurrentRate()})
}
