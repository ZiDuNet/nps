package conn

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/rate"
)

const (
	// maxFramedDatagramSize is larger than any valid IPv4/IPv6 UDP payload
	// while still bounding a malformed or hostile length prefix.
	maxFramedDatagramSize = 1 << 20
)

var errFramedDatagramTooLarge = errors.New("framed datagram exceeds the maximum size")

// FramedConn turns an arbitrary byte stream into a datagram-oriented
// ReadWriteCloser. Write frames one payload, Read returns one payload and
// preserves boundaries even when the caller's buffer is smaller than a
// datagram. The wire format is a 4-byte little-endian payload length
// followed by the payload, matching the rest of the NPS wire encoding.
type FramedConn struct {
	rwc     io.ReadWriteCloser
	writeMu sync.Mutex
	readMu  sync.Mutex
	pending []byte
	header  [4]byte
}

func NewFramedConn(rwc io.ReadWriteCloser) *FramedConn {
	return &FramedConn{rwc: rwc}
}

func (f *FramedConn) Read(p []byte) (int, error) {
	if f == nil || f.rwc == nil {
		return 0, io.ErrClosedPipe
	}
	f.readMu.Lock()
	defer f.readMu.Unlock()
	if len(f.pending) > 0 {
		n := copy(p, f.pending)
		f.pending = f.pending[n:]
		return n, nil
	}
	if _, err := io.ReadFull(f.rwc, f.header[:]); err != nil {
		return 0, err
	}
	size := int(binary.LittleEndian.Uint32(f.header[:]))
	if size > maxFramedDatagramSize {
		return 0, errFramedDatagramTooLarge
	}
	if size == 0 {
		return 0, nil
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(f.rwc, buf); err != nil {
		return 0, err
	}
	n := copy(p, buf)
	if n < size {
		f.pending = buf[n:]
	}
	return n, nil
}

func (f *FramedConn) Write(p []byte) (int, error) {
	if f == nil || f.rwc == nil {
		return 0, io.ErrClosedPipe
	}
	if len(p) > maxFramedDatagramSize {
		return 0, errFramedDatagramTooLarge
	}
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	binary.LittleEndian.PutUint32(f.header[:], uint32(len(p)))
	if _, err := f.rwc.Write(f.header[:]); err != nil {
		return 0, err
	}
	return f.rwc.Write(p)
}

// CopyDatagrams relays length-framed datagrams on streamSide to raw
// datagrams on datagramSide and back. datagramSide must be a connected UDP
// socket so Read and Write each transfer exactly one datagram. Both
// connections are closed when either direction ends.
func CopyDatagrams(streamSide, datagramSide net.Conn, crypt, snappy bool, rt *rate.Rate,
	isServer bool, task *file.Tunnel, host *file.Host, tlsFingerprint ...string) {
	if streamSide == nil || datagramSide == nil {
		return
	}
	var closeOnce sync.Once
	closeBoth := func() {
		closeOnce.Do(func() {
			_ = streamSide.Close()
			_ = datagramSide.Close()
		})
	}
	defer closeBoth()
	rwc := GetConnWithLimits(streamSide, crypt, snappy, rt, isServer, task, host, tlsFingerprint...)
	framed := NewFramedConn(rwc)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer closeBoth()
		copyDatagramToStream(framed, datagramSide)
	}()
	copyDatagramFromStream(framed, datagramSide)
	closeBoth()
	wg.Wait()
}

// copyDatagramFromStream reads framed datagrams and writes each one to the
// connected UDP socket.
func copyDatagramFromStream(framed *FramedConn, datagram net.Conn) {
	buf := common.BufPool.Get().([]byte)
	defer common.BufPool.Put(buf)
	for {
		n, err := framed.Read(buf)
		if n > 0 {
			if _, werr := datagram.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// copyDatagramToStream reads raw datagrams from the connected UDP socket and
// writes each one as a frame on the stream.
func copyDatagramToStream(framed *FramedConn, datagram net.Conn) {
	buf := common.BufPool.Get().([]byte)
	defer common.BufPool.Put(buf)
	for {
		n, err := datagram.Read(buf)
		if n > 0 {
			if _, werr := framed.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
