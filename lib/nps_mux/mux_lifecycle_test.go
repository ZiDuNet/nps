package nps_mux

import (
	"io"
	"math"
	"net"
	"sync"
	"testing"
	"time"
)

func TestLatencyCounterReturnsFiniteAverage(t *testing.T) {
	counter := newLatencyCounter()
	if got := counter.Latency(10); got != 10 {
		t.Fatalf("first latency = %v, want 10", got)
	}
	if got := counter.Latency(20); got != 15 {
		t.Fatalf("average latency = %v, want 15", got)
	}
	if got := counter.Latency(0); got != 0 || math.IsNaN(got) {
		t.Fatalf("invalid latency result = %v, want 0", got)
	}
}

func TestMuxCloseUnblocksAcceptWithoutClosingProducerChannel(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	mux := NewMux(server, "tcp", 1)

	accepted := make(chan error, 1)
	go func() {
		_, err := mux.Accept()
		accepted <- err
	}()

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mux.Close()
		}()
	}
	wg.Wait()

	select {
	case err := <-accepted:
		if err == nil {
			t.Fatal("Accept returned a connection after mux close")
		}
	case <-time.After(time.Second):
		t.Fatal("Accept did not unblock after mux close")
	}
}

func TestConnMapCloseDoesNotMutateDuringIteration(t *testing.T) {
	mux := &Mux{closeChan: make(chan struct{}), connMap: NewConnMap()}
	first := NewConn(1, mux)
	second := NewConn(2, mux)
	mux.connMap.Set(first.connId, first)
	mux.connMap.Set(second.connId, second)

	mux.isClose.Store(true)
	mux.connMap.Close()
	if got := mux.connMap.Size(); got != 0 {
		t.Fatalf("connection map size after Close = %d, want 0", got)
	}
}

func TestReceiveWindowCloseDoesNotDeadlockWithEOFReader(t *testing.T) {
	mux := &Mux{closeChan: make(chan struct{}), connMap: NewConnMap()}
	window := new(receiveWindow)
	window.New(mux)

	readDone := make(chan struct{})
	go func() {
		_, _ = window.Read(make([]byte, 1), 1)
		close(readDone)
	}()

	closeDone := make(chan struct{})
	go func() {
		window.CloseWindow()
		close(closeDone)
	}()

	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("receive window close deadlocked with an EOF reader")
	}
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("EOF reader did not return after receive window close")
	}
}

func TestMuxRoundTrip(t *testing.T) {
	server, client := net.Pipe()
	left := NewMux(server, "tcp", 1)
	right := NewMux(client, "tcp", 1)
	t.Cleanup(func() {
		_ = left.Close()
		_ = right.Close()
	})

	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		connection, err := right.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- connection
	}()

	local, err := left.NewConn()
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}
	defer local.Close()

	var remote net.Conn
	select {
	case remote = <-accepted:
	case err := <-acceptErr:
		t.Fatalf("Accept: %v", err)
	case <-time.After(time.Second):
		t.Fatal("Accept did not receive the new connection")
	}
	defer remote.Close()

	const payload = "review"
	if _, err := local.Write([]byte(payload)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := remote.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(remote, buf); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if got := string(buf); got != payload {
		t.Fatalf("payload = %q, want %q", got, payload)
	}
}
