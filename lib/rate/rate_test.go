package rate

import (
	"testing"
	"time"
)

func TestRateSharesOneLimiterAndAllowsUnlimitedMode(t *testing.T) {
	limited := NewRate(100)
	limited.Get(100) // Consume the initial token-bucket burst.
	started := time.Now()
	limited.Get(20)
	if elapsed := time.Since(started); elapsed < 120*time.Millisecond {
		t.Fatalf("second transfer waited %s, want shared rate limiter to delay it", elapsed)
	}

	unlimited := NewRate(0)
	started = time.Now()
	unlimited.Get(1 << 20)
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("unlimited mode unexpectedly waited %s", elapsed)
	}
}
