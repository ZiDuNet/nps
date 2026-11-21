package rate

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Shared is one token bucket used by both directions. A single configured
// ceiling therefore covers upload plus download, rather than granting the
// same ceiling independently to each direction.
type Shared struct {
	// Keep atomically accessed 64-bit fields at the start of the allocation.
	// This guarantees their required 8-byte alignment on 32-bit platforms.
	_             [0]atomic.Int64
	uploadBytes   int64
	downloadBytes int64
	mu            sync.Mutex
	limiter       *Rate
	uploadRate    int64
	downloadRate  int64
	sampleAt      time.Time
}

func NewShared(limit int64) *Shared {
	return &Shared{limiter: NewStandaloneRate(normalizeLimit(limit)), sampleAt: time.Now()}
}

func (s *Shared) limiterForUse() *Rate {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.limiter == nil {
		s.limiter = NewStandaloneRate(0)
	}
	limiter := s.limiter
	if s.sampleAt.IsZero() {
		s.sampleAt = time.Now()
	}
	s.mu.Unlock()
	return limiter
}

func (s *Shared) SetLimit(limit int64) {
	if s == nil {
		return
	}
	s.limiterForUse().ResetLimit(normalizeLimit(limit))
}

func (s *Shared) Limit() int64 {
	if s == nil {
		return 0
	}
	return s.limiterForUse().Limit()
}

func (s *Shared) Upload(size int64) {
	if s == nil || size <= 0 {
		return
	}
	s.limiterForUse().Get(size)
	atomic.AddInt64(&s.uploadBytes, size)
	s.sample()
}

func (s *Shared) Download(size int64) {
	if s == nil || size <= 0 {
		return
	}
	s.limiterForUse().Get(size)
	atomic.AddInt64(&s.downloadBytes, size)
	s.sample()
}

func (s *Shared) sample() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.sampleAt.IsZero() {
		s.sampleAt = now
		return
	}
	elapsed := now.Sub(s.sampleAt)
	if elapsed < time.Second {
		return
	}
	upload := atomic.SwapInt64(&s.uploadBytes, 0)
	download := atomic.SwapInt64(&s.downloadBytes, 0)
	seconds := elapsed.Seconds()
	s.uploadRate = int64(float64(upload) / seconds)
	s.downloadRate = int64(float64(download) / seconds)
	s.sampleAt = now
}

func (s *Shared) Throughput() (upload, download int64) {
	if s == nil {
		return 0, 0
	}
	s.sample()
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.sampleAt) > 2*time.Second {
		return 0, 0
	}
	return s.uploadRate, s.downloadRate
}

type Limiter interface {
	Upload(size int64)
	Download(size int64)
}

func NewSharedConn(conn io.ReadWriteCloser, limiter *Shared) io.ReadWriteCloser {
	if conn == nil || limiter == nil {
		return conn
	}
	limiter.limiterForUse()
	return &sharedConn{ReadWriteCloser: conn, limiter: limiter}
}

type sharedConn struct {
	io.ReadWriteCloser
	limiter *Shared
}

func (c *sharedConn) Read(p []byte) (int, error) {
	n, err := c.ReadWriteCloser.Read(p)
	if n > 0 {
		c.limiter.Download(int64(n))
	}
	return n, err
}

func (c *sharedConn) Write(p []byte) (int, error) {
	if len(p) > 0 {
		c.limiter.Upload(int64(len(p)))
	}
	return c.ReadWriteCloser.Write(p)
}

func (c *sharedConn) LocalAddr() net.Addr {
	if value, ok := c.ReadWriteCloser.(net.Conn); ok {
		return value.LocalAddr()
	}
	return nil
}

func (c *sharedConn) RemoteAddr() net.Addr {
	if value, ok := c.ReadWriteCloser.(net.Conn); ok {
		return value.RemoteAddr()
	}
	return nil
}

func (c *sharedConn) SetDeadline(t time.Time) error {
	if value, ok := c.ReadWriteCloser.(interface{ SetDeadline(time.Time) error }); ok {
		return value.SetDeadline(t)
	}
	return nil
}

func (c *sharedConn) SetReadDeadline(t time.Time) error {
	if value, ok := c.ReadWriteCloser.(interface {
		SetReadDeadline(time.Time) error
	}); ok {
		return value.SetReadDeadline(t)
	}
	return nil
}

func (c *sharedConn) SetWriteDeadline(t time.Time) error {
	if value, ok := c.ReadWriteCloser.(interface {
		SetWriteDeadline(time.Time) error
	}); ok {
		return value.SetWriteDeadline(t)
	}
	return nil
}
