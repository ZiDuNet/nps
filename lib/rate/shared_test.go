package rate

import (
	"testing"
	"time"
)

func TestShared64BitAtomicsWorkAcrossDirections(t *testing.T) {
	limiter := NewShared(0)
	limiter.Upload(16)
	limiter.Download(32)
	limiter.mu.Lock()
	limiter.sampleAt = time.Now().Add(-2 * time.Second)
	limiter.mu.Unlock()

	upload, download := limiter.Throughput()
	if upload <= 0 || download <= 0 {
		t.Fatalf("throughput = %d/%d, want positive samples", upload, download)
	}
}
