package rate

import (
	"bytes"
	"io"
	"testing"
	"time"
)

type directionalRWC struct{ bytes.Buffer }

func (directionalRWC) Close() error { return nil }

func TestDirectionalTracksIndependentRates(t *testing.T) {
	d := NewDirectional(0, 0)
	c := NewDirectionalConn(&directionalRWC{Buffer: *bytes.NewBufferString("download")}, d)
	if _, err := c.Write([]byte("upload")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	_, _ = c.Read(buf)
	deadline := time.Now().Add(1500 * time.Millisecond)
	var up, down int64
	for time.Now().Before(deadline) {
		up, down = d.Throughput()
		if up > 0 && down > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if up == 0 || down == 0 {
		t.Fatalf("throughput = %d/%d, want both directions recorded", up, down)
	}
}

func TestDirectionalLimitsCanBeUpdated(t *testing.T) {
	d := NewDirectional(1024, 2048)
	up, down := d.Limits()
	if up != 1024 || down != 2048 {
		t.Fatalf("initial limits = %d/%d", up, down)
	}
	d.SetLimits(0, 4096)
	up, down = d.Limits()
	if up != 0 || down != 4096 {
		t.Fatalf("updated limits = %d/%d", up, down)
	}
}

func TestDirectionalNilConnectionIsSafe(t *testing.T) {
	if got := NewDirectionalConn(nil, NewDirectional(0, 0)); got != nil {
		t.Fatal("nil connection should remain nil")
	}
	if _, err := io.Copy(io.Discard, bytes.NewReader(nil)); err != nil {
		t.Fatal(err)
	}
}
