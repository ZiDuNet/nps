package nps_mux

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Rate is the small token bucket used by the mux benchmark/compatibility
// connection. Access to the bucket is serialized so concurrent readers and
// writers cannot overspend tokens or race with shutdown.
type Rate struct {
	bucketSize        int64
	bucketSurplusSize int64
	bucketAddSize     int64
	stopChan          chan struct{}
	stopOnce          sync.Once
	startOnce         sync.Once
	mu                sync.Mutex
	cond              *sync.Cond
	stopped           bool
	NowRate           int64
}

func NewRate(addSize int64) *Rate {
	if addSize < 0 {
		addSize = 0
	}
	maxInt64 := int64(^uint64(0) >> 1)
	bucketSize := addSize
	if bucketSize > maxInt64/2 {
		bucketSize = maxInt64
	} else {
		bucketSize *= 2
	}
	r := &Rate{
		bucketSize:    bucketSize,
		bucketAddSize: addSize,
		stopChan:      make(chan struct{}),
	}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (s *Rate) Start() {
	if s == nil || s.stopChan == nil {
		return
	}
	s.startOnce.Do(func() {
		go s.session()
	})
}

func (s *Rate) add(size int64) {
	if s == nil || size <= 0 {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	available := s.bucketSize - s.bucketSurplusSize
	if size > available {
		size = available
	}
	if size > 0 {
		s.bucketSurplusSize += size
		s.cond.Broadcast()
	}
	s.mu.Unlock()
}

// ReturnBucket returns tokens to the bucket, capped at its configured size.
func (s *Rate) ReturnBucket(size int64) {
	s.add(size)
}

// Stop wakes blocked Get calls and makes repeated stops harmless.
func (s *Rate) Stop() {
	if s == nil || s.stopChan == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopChan)
		s.mu.Lock()
		s.stopped = true
		s.cond.Broadcast()
		s.mu.Unlock()
	})
}

// Get waits for enough tokens to account for size. Requests larger than the
// bucket are consumed in chunks, so a large mux frame cannot wait forever.
func (s *Rate) Get(size int64) {
	if s == nil || size <= 0 || s.bucketAddSize <= 0 || s.cond == nil {
		return
	}
	for remaining := size; remaining > 0; {
		chunk := remaining
		if chunk > s.bucketSize {
			chunk = s.bucketSize
		}
		s.mu.Lock()
		for s.bucketSurplusSize < chunk && !s.stopped {
			s.cond.Wait()
		}
		if s.stopped {
			s.mu.Unlock()
			return
		}
		s.bucketSurplusSize -= chunk
		s.mu.Unlock()
		remaining -= chunk
	}
}

func (s *Rate) session() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.mu.Lock()
			if s.stopped {
				s.mu.Unlock()
				return
			}
			available := s.bucketSize - s.bucketSurplusSize
			added := s.bucketAddSize
			if added > available {
				added = available
			}
			if added > 0 {
				s.bucketSurplusSize += added
			}
			atomic.StoreInt64(&s.NowRate, added)
			s.cond.Broadcast()
			s.mu.Unlock()
		case <-s.stopChan:
			return
		}
	}
}

// CurrentRate returns the most recent one-second refill sample.
func (s *Rate) CurrentRate() int64 {
	if s == nil {
		return 0
	}
	return atomic.LoadInt64(&s.NowRate)
}

type Conn struct {
	conn net.Conn
	rate *Rate
}

func NewRateConn(rate *Rate, conn net.Conn) *Conn {
	return &Conn{conn: conn, rate: rate}
}

func (conn *Conn) Read(b []byte) (n int, err error) {
	if conn == nil || conn.conn == nil {
		return 0, net.ErrClosed
	}
	n, err = conn.conn.Read(b)
	if conn.rate != nil {
		conn.rate.Get(int64(n))
	}
	return
}

func (conn *Conn) Write(b []byte) (n int, err error) {
	if conn == nil || conn.conn == nil {
		return 0, net.ErrClosed
	}
	n, err = conn.conn.Write(b)
	if conn.rate != nil {
		conn.rate.Get(int64(n))
	}
	return
}

func (conn *Conn) LocalAddr() net.Addr {
	if conn == nil || conn.conn == nil {
		return nil
	}
	return conn.conn.LocalAddr()
}

func (conn *Conn) RemoteAddr() net.Addr {
	if conn == nil || conn.conn == nil {
		return nil
	}
	return conn.conn.RemoteAddr()
}

func (conn *Conn) SetDeadline(t time.Time) error {
	if conn == nil || conn.conn == nil {
		return net.ErrClosed
	}
	return conn.conn.SetDeadline(t)
}

func (conn *Conn) SetWriteDeadline(t time.Time) error {
	if conn == nil || conn.conn == nil {
		return net.ErrClosed
	}
	return conn.conn.SetWriteDeadline(t)
}

func (conn *Conn) SetReadDeadline(t time.Time) error {
	if conn == nil || conn.conn == nil {
		return net.ErrClosed
	}
	return conn.conn.SetReadDeadline(t)
}

func (conn *Conn) Close() error {
	if conn == nil || conn.conn == nil {
		return nil
	}
	return conn.conn.Close()
}
