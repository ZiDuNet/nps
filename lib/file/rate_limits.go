package file

import (
	"sync"

	"ehang.io/nps/lib/rate"
)

// RateLimits stores per-resource limits in bits per second. Runtime buckets
// are not persisted; zero means unlimited.
type RateLimits struct {
	UploadLimit   int64 `json:"UploadLimit,omitempty"`
	DownloadLimit int64 `json:"DownloadLimit,omitempty"`
	rateMu        sync.Mutex
	runtimeRate   *rate.Directional
}

func limitBytes(bps int64) int64 {
	return rate.BytesPerSecondFromBps(bps)
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

// SetRateLimits applies new bps values to existing and future connections.
func (r *RateLimits) SetRateLimits(uploadBps, downloadBps int64) {
	if r == nil {
		return
	}
	if uploadBps < 0 {
		uploadBps = 0
	}
	if downloadBps < 0 {
		downloadBps = 0
	}
	r.rateMu.Lock()
	r.UploadLimit, r.DownloadLimit = uploadBps, downloadBps
	if r.runtimeRate != nil {
		r.runtimeRate.SetLimits(limitBytes(uploadBps), limitBytes(downloadBps))
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

// RateLimitSnapshot returns a race-safe copy of the configured bps values.
func (r *RateLimits) RateLimitSnapshot() (upload, download int64) {
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
