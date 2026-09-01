package pmux

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestPortConnReadPreservesBufferedPrefix(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	conn := newPortConn(server, []byte("abcdef"), true)
	first := make([]byte, 2)
	if n, err := conn.Read(first); err != nil || n != 2 || string(first) != "ab" {
		t.Fatalf("first read = %d/%q, %v", n, first, err)
	}

	go func() { _, _ = client.Write([]byte("ghi")) }()
	second := make([]byte, 7)
	if n, err := conn.Read(second); err != nil || n != 7 || string(second) != "cdefghi" {
		t.Fatalf("second read = %d/%q, %v", n, second, err)
	}
}

func TestPortMuxStartIsIdempotentAndCloseUnblocksListeners(t *testing.T) {
	mux := NewPortMux(0, "")
	if err := mux.Start(); err != nil {
		t.Fatalf("second Start failed: %v", err)
	}
	addr := mux.listenerAddr()
	if addr == nil {
		t.Fatal("port mux did not expose a listener address")
	}

	listener := mux.GetHttpListener()
	accepted := make(chan error, 1)
	go func() {
		_, err := listener.Accept()
		accepted <- err
	}()

	if err := mux.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	select {
	case err := <-accepted:
		if err == nil {
			t.Fatal("closed listener returned a connection")
		}
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("closed listener error = %v, want net.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("listener Accept did not unblock after Close")
	}
	if err := mux.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}
	if _, err := mux.GetHttpListener().Accept(); err == nil {
		t.Fatal("listener created after Close unexpectedly accepted")
	}
}

func TestPortMuxRoutesClientConnection(t *testing.T) {
	mux := NewPortMux(0, "")
	defer mux.Close()
	addr := mux.listenerAddr()
	if addr == nil {
		t.Fatal("port mux did not expose a listener address")
	}

	listener := mux.GetClientListener()
	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()

	client, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatalf("dial mux: %v", err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("TSTpayload")); err != nil {
		t.Fatalf("write protocol marker: %v", err)
	}
	select {
	case err := <-acceptErr:
		t.Fatalf("accept client connection: %v", err)
	case conn := <-accepted:
		defer conn.Close()
		buf := make([]byte, 3)
		if _, err := io.ReadFull(conn, buf); err != nil {
			t.Fatalf("read buffered marker: %v", err)
		}
		if string(buf) != "TST" {
			t.Fatalf("marker = %q", buf)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for client connection")
	}
}

func TestPortMuxBindsConfiguredAddress(t *testing.T) {
	mux := NewPortMuxWithAddress(0, "", "127.0.0.1")
	defer mux.Close()

	addr, ok := mux.listenerAddr().(*net.TCPAddr)
	if !ok || addr == nil {
		t.Fatalf("listener address = %T %v, want *net.TCPAddr", mux.listenerAddr(), mux.listenerAddr())
	}
	if !addr.IP.IsLoopback() {
		t.Fatalf("listener IP = %v, want loopback", addr.IP)
	}
}
