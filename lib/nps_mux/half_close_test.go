package nps_mux

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestMuxRemoteCloseDrainsQueuedFrames(t *testing.T) {
	leftTransport, rightTransport := net.Pipe()
	left := NewMux(leftTransport, "tcp", 1)
	right := NewMux(rightTransport, "tcp", 1)
	t.Cleanup(func() {
		_ = left.Close()
		_ = right.Close()
	})

	accepted := make(chan net.Conn, 1)
	go func() {
		connection, err := right.Accept()
		if err == nil {
			accepted <- connection
		}
	}()

	sender, err := left.NewConn()
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}
	defer sender.Close()

	var receiver *conn
	select {
	case acceptedConn := <-accepted:
		var ok bool
		receiver, ok = acceptedConn.(*conn)
		if !ok {
			t.Fatalf("accepted connection type = %T, want *conn", acceptedConn)
		}
	case <-time.After(time.Second):
		t.Fatal("Accept did not receive the new connection")
	}
	defer receiver.Close()

	payload := bytes.Repeat([]byte("nps-half-close-"), 1024)
	if n, err := sender.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("write payload: n=%d err=%v", n, err)
	}
	if err := sender.Close(); err != nil {
		t.Fatalf("close sender: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for !receiver.receiveWindow.inputClosed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !receiver.receiveWindow.inputClosed.Load() {
		t.Fatal("receiver did not observe remote close")
	}
	if receiver.isClose.Load() {
		t.Fatal("remote close released queued frames before they could be read")
	}

	got, err := io.ReadAll(receiver)
	if err != nil {
		t.Fatalf("read queued payload: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload was truncated: got %d bytes, want %d", len(got), len(payload))
	}

	deadline = time.Now().Add(time.Second)
	for right.connMap.Size() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := right.connMap.Size(); got != 0 {
		t.Fatalf("drained stream remains in connection map: %d", got)
	}
}

func TestMuxRemoteCloseReleasesUnreadStreamAfterTimeout(t *testing.T) {
	previousTimeout := remoteCloseDrainTimeout
	remoteCloseDrainTimeout = 25 * time.Millisecond
	t.Cleanup(func() { remoteCloseDrainTimeout = previousTimeout })

	leftTransport, rightTransport := net.Pipe()
	left := NewMux(leftTransport, "tcp", 1)
	right := NewMux(rightTransport, "tcp", 1)
	t.Cleanup(func() {
		_ = left.Close()
		_ = right.Close()
	})

	accepted := make(chan net.Conn, 1)
	go func() {
		connection, err := right.Accept()
		if err == nil {
			accepted <- connection
		}
	}()

	sender, err := left.NewConn()
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}
	defer sender.Close()

	select {
	case receiver := <-accepted:
		defer receiver.Close()
	case <-time.After(time.Second):
		t.Fatal("Accept did not receive the new connection")
	}

	if err := sender.Close(); err != nil {
		t.Fatalf("close sender: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for right.connMap.Size() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := right.connMap.Size(); got != 0 {
		t.Fatalf("unread peer-closed stream was not reclaimed: %d", got)
	}
}
