package nps_mux

import (
	"sync/atomic"
	"testing"
)

func TestRate64BitAtomicReporting(t *testing.T) {
	limiter := NewRate(1024)
	atomic.StoreInt64(&limiter.NowRate, 2048)
	if got := limiter.CurrentRate(); got != 2048 {
		t.Fatalf("current rate = %d, want 2048", got)
	}
}
