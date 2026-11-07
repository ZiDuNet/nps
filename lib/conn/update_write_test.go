package conn

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConcurrentUpdateAndHealthReportsKeepFrameBoundaries(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	writer, reader := NewConn(left), NewConn(right)
	_ = right.SetReadDeadline(time.Now().Add(5 * time.Second))
	const count = 50
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 2*count; i++ {
			info, _, err := reader.GetHealthInfo()
			if err != nil {
				done <- err
				return
			}
			if !strings.HasPrefix(info, "nps:update:v2:") && info != "target:8080" {
				t.Errorf("interleaved info=%q", info)
			}
		}
		done <- nil
	}()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < count; i++ {
			_, err := writer.SendHealthInfo("target:8080", "true")
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < count; i++ {
			frame, _ := GetLenBytes([]byte("nps:update:v2:eyJqb2IiOiIxIn0*#*true"))
			if _, err := writer.WriteTimeout(frame, time.Second); err != nil {
				return
			}
		}
	}()
	wg.Wait()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// The updater's deadline must not poison later writes.
	go func() { _, _ = writer.SendHealthInfo("target:8080", "true") }()
	if _, _, err := reader.GetHealthInfo(); err != nil {
		t.Fatal("deadline leaked", err)
	}
}
