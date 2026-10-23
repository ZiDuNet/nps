package rate

import (
	"io"
)

type rateConn struct {
	conn io.ReadWriteCloser
	rate *Rate
}

// NewRateConn preserves the legacy client-wide aggregate limiter and can
// optionally layer resource-scoped directional limiters. Reads are backend to
// visitor (download); writes are visitor to backend (upload).
func NewRateConn(conn io.ReadWriteCloser, rate *Rate, entry ...*Directional) io.ReadWriteCloser {
	for _, limiter := range entry {
		conn = NewDirectionalConn(conn, limiter)
	}
	return &rateConn{
		conn: conn,
		rate: rate,
	}
}

func (s *rateConn) Read(b []byte) (n int, err error) {
	n, err = s.conn.Read(b)
	if s.rate != nil {
		s.rate.Get(int64(n))
	}
	return
}

func (s *rateConn) Write(b []byte) (n int, err error) {
	n, err = s.conn.Write(b)
	if s.rate != nil {
		s.rate.Get(int64(n))
	}
	return
}

func (s *rateConn) Close() error {
	return s.conn.Close()
}
