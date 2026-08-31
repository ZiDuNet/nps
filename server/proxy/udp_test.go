package proxy

import (
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type udpTestConn struct {
	closed atomic.Bool
}

func (c *udpTestConn) Read([]byte) (int, error)    { return 0, io.EOF }
func (c *udpTestConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *udpTestConn) Close() error {
	c.closed.Store(true)
	return nil
}

func TestUDPSessionTargetLifecycleIsSynchronized(t *testing.T) {
	sess := &udpSession{}
	target := &udpTestConn{}
	if !sess.installTarget(target) {
		t.Fatal("first target should install")
	}

	var readers sync.WaitGroup
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for j := 0; j < 200; j++ {
				_ = sess.getTarget()
			}
		}()
	}
	sess.closeTarget()
	readers.Wait()

	if !target.closed.Load() {
		t.Fatal("closing a session should close its published target")
	}
	late := &udpTestConn{}
	if sess.installTarget(late) {
		t.Fatal("a closed session must reject late target installation")
	}
	if !late.closed.Load() {
		t.Fatal("a rejected late target must be closed")
	}

	// Repeated cleanup is intentionally idempotent.
	sess.closeTarget()
}

func TestUDPSessionBuildErrorIsSynchronized(t *testing.T) {
	sess := &udpSession{}
	want := io.ErrUnexpectedEOF
	sess.setError(want)
	if got := sess.buildError(); got != want {
		t.Fatalf("buildError() = %v, want %v", got, want)
	}
}

type udpWriteProbe struct {
	active     atomic.Int32
	max        atomic.Int32
	concurrent atomic.Bool
}

func (p *udpWriteProbe) Read([]byte) (int, error) { return 0, io.EOF }

func (p *udpWriteProbe) Write(data []byte) (int, error) {
	active := p.active.Add(1)
	for {
		max := p.max.Load()
		if active <= max || p.max.CompareAndSwap(max, active) {
			break
		}
	}
	if active > 1 {
		p.concurrent.Store(true)
	}
	time.Sleep(time.Millisecond)
	p.active.Add(-1)
	return len(data), nil
}

func (p *udpWriteProbe) Close() error { return nil }

func TestUDPSessionSerializesMuxWrites(t *testing.T) {
	sess := &udpSession{}
	probe := &udpWriteProbe{}
	if !sess.installTarget(probe) {
		t.Fatal("target should install")
	}
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 8; j++ {
				if _, err := sess.write([]byte("packet")); err != nil {
					t.Errorf("serialized write failed: %v", err)
				}
				runtime.Gosched()
			}
		}()
	}
	workers.Wait()
	if probe.concurrent.Load() {
		t.Fatal("UDP session allowed concurrent writes to one mux stream")
	}
	sess.closeTarget()
}

func TestUDPServerCloseBeforeStartIsSafe(t *testing.T) {
	server := NewUdpModeServer(nil, nil)
	if err := server.Close(); err != nil {
		t.Fatalf("closing an unstarted UDP server should be harmless: %v", err)
	}
}
