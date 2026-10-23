package rate

import (
	"io"
	"sync"
)

// Directional provides independent, shared upload and download buckets for a
// tunnel or HTTP host. Limits are bytes per second; zero means unlimited.
// The runtime buckets are intentionally not serialized with the resource
// model, so a restart starts with a clean limiter while preserving config.
type Directional struct {
	mu       sync.Mutex
	upload   *Rate
	download *Rate
}

// NewDirectional creates a resource limiter with byte-per-second limits.
func NewDirectional(upload, download int64) *Directional {
	d := &Directional{
		upload:   NewStandaloneRate(normalizeLimit(upload)),
		download: NewStandaloneRate(normalizeLimit(download)),
	}
	return d
}

func normalizeLimit(limit int64) int64 {
	if limit < 0 {
		return 0
	}
	return limit
}

func (d *Directional) rates() (upload, download *Rate) {
	if d == nil {
		return nil, nil
	}
	d.mu.Lock()
	if d.upload == nil {
		d.upload = NewStandaloneRate(0)
	}
	if d.download == nil {
		d.download = NewStandaloneRate(0)
	}
	upload, download = d.upload, d.download
	d.mu.Unlock()
	return upload, download
}

// SetLimits changes both buckets in place so already-open connections receive
// edits immediately.
func (d *Directional) SetLimits(upload, download int64) {
	if d == nil {
		return
	}
	uploadRate, downloadRate := d.rates()
	uploadRate.ResetLimit(normalizeLimit(upload))
	downloadRate.ResetLimit(normalizeLimit(download))
}

func (d *Directional) Upload(size int64) {
	if d == nil || size <= 0 {
		return
	}
	upload, _ := d.rates()
	upload.Get(size)
}

func (d *Directional) Download(size int64) {
	if d == nil || size <= 0 {
		return
	}
	_, download := d.rates()
	download.Get(size)
}

// Limits returns the configured upload and download limits in bytes per
// second.
func (d *Directional) Limits() (upload, download int64) {
	if d == nil {
		return 0, 0
	}
	uploadRate, downloadRate := d.rates()
	return uploadRate.Limit(), downloadRate.Limit()
}

// Throughput returns the most recent one-second upload/download samples.
func (d *Directional) Throughput() (upload, download int64) {
	if d == nil {
		return 0, 0
	}
	uploadRate, downloadRate := d.rates()
	return uploadRate.CurrentRate(), downloadRate.CurrentRate()
}

// NewDirectionalConn applies upload throttling to writes and download
// throttling to reads. The wrapper keeps the original closer semantics.
func NewDirectionalConn(conn io.ReadWriteCloser, limiter *Directional) io.ReadWriteCloser {
	if conn == nil || limiter == nil {
		return conn
	}
	limiter.rates()
	return &directionalConn{ReadWriteCloser: conn, limiter: limiter}
}

type directionalConn struct {
	io.ReadWriteCloser
	limiter *Directional
}

func (c *directionalConn) Read(p []byte) (int, error) {
	n, err := c.ReadWriteCloser.Read(p)
	if n > 0 {
		c.limiter.Download(int64(n))
	}
	return n, err
}

func (c *directionalConn) Write(p []byte) (int, error) {
	if len(p) > 0 {
		c.limiter.Upload(int64(len(p)))
	}
	return c.ReadWriteCloser.Write(p)
}
