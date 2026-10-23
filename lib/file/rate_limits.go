package file

import (
	"math"
	"sync"

	"ehang.io/nps/lib/rate"
)

// RateLimits stores per-resource limits in KB/s. Runtime buckets are not
// persisted; zero means unlimited and remains compatible with old JSON files.
type RateLimits struct {
	UploadLimit   int `json:"UploadLimit,omitempty"`
	DownloadLimit int `json:"DownloadLimit,omitempty"`
	rateMu        sync.Mutex
	runtimeRate   *rate.Directional
}

func limitBytes(k int) int64 {
	if k <= 0 {
		return 0
	}
	if uint64(k) > uint64(math.MaxInt64/1024) {
		return math.MaxInt64
	}
	return int64(k) * 1024
}

// RateLimiter returns the shared runtime buckets for this resource.
func (r *RateLimits) RateLimiter() *rate.Directional {
	if r == nil {
		return nil
	}
	r.rateMu.Lock()
	defer r.rateMu.Unlock()
	if r.runtimeRate == nil {
		r.runtimeRate = rate.NewDirectional(limitBytes(r.UploadLimit), limitBytes(r.DownloadLimit))
	}
	return r.runtimeRate
}

// SetRateLimits applies new values to existing and future connections.
func (r *RateLimits) SetRateLimits(upload, download int) {
	if r == nil {
		return
	}
	if upload < 0 {
		upload = 0
	}
	if download < 0 {
		download = 0
	}
	r.rateMu.Lock()
	r.UploadLimit, r.DownloadLimit = upload, download
	if r.runtimeRate != nil {
		r.runtimeRate.SetLimits(limitBytes(upload), limitBytes(download))
	}
	r.rateMu.Unlock()
}

func (r *RateLimits) ClearUploadLimit() {
	if r == nil {
		return
	}
	r.rateMu.Lock()
	down := r.DownloadLimit
	r.rateMu.Unlock()
	r.SetRateLimits(0, down)
}

func (r *RateLimits) ClearDownloadLimit() {
	if r == nil {
		return
	}
	r.rateMu.Lock()
	up := r.UploadLimit
	r.rateMu.Unlock()
	r.SetRateLimits(up, 0)
}

func (r *RateLimits) CurrentRates() (int64, int64) {
	if r == nil {
		return 0, 0
	}
	r.rateMu.Lock()
	d := r.runtimeRate
	r.rateMu.Unlock()
	if d == nil {
		return 0, 0
	}
	return d.Throughput()
}

// RateLimitSnapshot returns a race-safe copy of the configured KB/s values.
func (r *RateLimits) RateLimitSnapshot() (upload, download int) {
	if r == nil {
		return 0, 0
	}
	r.rateMu.Lock()
	upload, download = r.UploadLimit, r.DownloadLimit
	r.rateMu.Unlock()
	return upload, download
}

func (r *RateLimits) copyConfig() RateLimits {
	if r == nil {
		return RateLimits{}
	}
	r.rateMu.Lock()
	defer r.rateMu.Unlock()
	return RateLimits{UploadLimit: r.UploadLimit, DownloadLimit: r.DownloadLimit}
}

// adopt preserves the active buckets when a controller replaces a resource
// object during an edit. Existing connections therefore keep their counters
// and observe the new limits immediately.
func (r *RateLimits) adopt(old *RateLimits) {
	if r == nil || old == nil {
		return
	}
	d := old.RateLimiter()
	r.rateMu.Lock()
	r.runtimeRate = d
	limitsUpload, limitsDownload := r.UploadLimit, r.DownloadLimit
	r.rateMu.Unlock()
	d.SetLimits(limitBytes(limitsUpload), limitBytes(limitsDownload))
}
