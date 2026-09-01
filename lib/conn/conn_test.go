package conn

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

type bytesThenErrConn struct {
	net.Conn
	payload []byte
	err     error
	read    bool
}

func (c *bytesThenErrConn) Read(b []byte) (int, error) {
	if c.read {
		return 0, c.err
	}
	c.read = true
	return copy(b, c.payload), c.err
}

func TestGetHostReturnsBufferedBytesOnReadError(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	payload := []byte("SSH-2.0-OpenSSH_9.0\r\n")
	if err := serverConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	type result struct {
		rb  []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		_, _, rb, err, _ := NewConn(serverConn).GetHost()
		done <- result{rb: rb, err: err}
	}()

	if _, err := clientConn.Write(payload); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-done:
		if got.err == nil {
			t.Fatal("expected GetHost to fail for non-HTTP payload")
		}
		if !bytes.Equal(got.rb, payload) {
			t.Fatalf("expected buffered payload %q, got %q", payload, got.rb)
		}
	case <-time.After(time.Second):
		t.Fatal("GetHost did not return after read deadline")
	}
}

func TestGetHostReturnsBufferedBytesWhenReadReturnsBytesAndError(t *testing.T) {
	payload := []byte("SSH-2.0-OpenSSH_9.0\r\n")
	c := &bytesThenErrConn{
		payload: payload,
		err:     io.ErrUnexpectedEOF,
	}

	_, _, rb, err, _ := NewConn(c).GetHost()
	if err == nil {
		t.Fatal("expected GetHost to fail for non-HTTP payload")
	}
	if !bytes.Equal(rb, payload) {
		t.Fatalf("expected buffered payload %q, got %q", payload, rb)
	}
}

func TestGetLinkInfoRejectsMalformedJSON(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	done := make(chan error, 1)
	go func() {
		_, err := NewConn(serverConn).GetLinkInfo()
		done <- err
	}()
	if err := binary.Write(clientConn, binary.LittleEndian, int32(3)); err != nil {
		t.Fatal(err)
	}
	if _, err := clientConn.Write([]byte("bad")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("malformed JSON was accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("GetLinkInfo did not return")
	}
}

func TestGetShortContentRejectsInvalidLength(t *testing.T) {
	if _, err := NewConn(nil).GetShortContent(-1); err == nil {
		t.Fatal("negative content length was accepted")
	}
	if _, err := NewConn(nil).GetShortContent((64 << 10) + 1); err == nil {
		t.Fatal("oversized content length was accepted")
	}
}
