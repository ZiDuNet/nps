package tool

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestGetServerStatusSamplesPadsEmptyHistory(t *testing.T) {
	withServerStatus(t, nil)

	samples := GetServerStatusSamples(10)
	if len(samples) != 10 {
		t.Fatalf("expected 10 samples, got %d", len(samples))
	}
	for index, sample := range samples {
		assertDashboardStatusFields(t, sample)
		if timeValue, ok := sample["time"].(string); !ok || timeValue != "" {
			t.Fatalf("sample %d should have an empty placeholder timestamp, got %#v", index, sample["time"])
		}
	}
}

func TestGetServerStatusSamplesCopiesStoredStatus(t *testing.T) {
	first := newSystemStatus(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC))
	first["sequence"] = "first"
	second := newSystemStatus(time.Date(2026, time.January, 2, 3, 5, 5, 0, time.UTC))
	second["sequence"] = "second"
	withServerStatus(t, []map[string]interface{}{first, second})

	samples := GetServerStatusSamples(10)
	if got := samples[0]["sequence"]; got != "first" {
		t.Fatalf("expected first status at the start of the sample, got %#v", got)
	}
	if got := samples[len(samples)-1]["sequence"]; got != "second" {
		t.Fatalf("expected second status at the end of the sample, got %#v", got)
	}

	samples[0]["sequence"] = "changed"
	ServerStatusLock.RLock()
	defer ServerStatusLock.RUnlock()
	if got := ServerStatus[0]["sequence"]; got != "first" {
		t.Fatalf("mutating a returned sample changed stored history: %#v", got)
	}
}

func TestGetServerStatusSamplesIncludeHistoryEndpoints(t *testing.T) {
	history := make([]map[string]interface{}, 20)
	for index := range history {
		history[index] = map[string]interface{}{"sequence": index}
	}
	withServerStatus(t, history)

	samples := GetServerStatusSamples(10)
	if got := samples[0]["sequence"]; got != 0 {
		t.Fatalf("expected first sampled sequence to be 0, got %#v", got)
	}
	if got := samples[len(samples)-1]["sequence"]; got != 19 {
		t.Fatalf("expected last sampled sequence to be 19, got %#v", got)
	}
}

func TestAppendServerStatusKeepsNewestHistory(t *testing.T) {
	withServerStatus(t, make([]map[string]interface{}, 0, serverStatusHistoryLimit))

	for sequence := 0; sequence <= serverStatusHistoryLimit; sequence++ {
		appendServerStatus(map[string]interface{}{"sequence": sequence})
	}

	ServerStatusLock.RLock()
	defer ServerStatusLock.RUnlock()
	if len(ServerStatus) != serverStatusHistoryLimit {
		t.Fatalf("expected %d history entries, got %d", serverStatusHistoryLimit, len(ServerStatus))
	}
	if got := ServerStatus[0]["sequence"]; got != 1 {
		t.Fatalf("expected oldest retained entry to be 1, got %#v", got)
	}
	if got := ServerStatus[len(ServerStatus)-1]["sequence"]; got != serverStatusHistoryLimit {
		t.Fatalf("expected newest retained entry to be %d, got %#v", serverStatusHistoryLimit, got)
	}
}

func TestServerStatusHistorySupportsConcurrentSnapshots(t *testing.T) {
	withServerStatus(t, make([]map[string]interface{}, 0, serverStatusHistoryLimit))

	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for sequence := 0; sequence < 500; sequence++ {
			appendServerStatus(map[string]interface{}{"sequence": sequence})
		}
	}()
	go func() {
		defer workers.Done()
		for index := 0; index < 500; index++ {
			for _, status := range GetServerStatusSamples(10) {
				assertDashboardStatusFields(t, status)
			}
		}
	}()
	workers.Wait()
}

func TestNewSystemStatusProvidesDashboardDefaults(t *testing.T) {
	status := newSystemStatus(time.Time{})
	assertDashboardStatusFields(t, status)

	var loadValues map[string]float64
	if err := json.Unmarshal([]byte(status["load"].(string)), &loadValues); err != nil {
		t.Fatalf("default load value must be valid JSON: %v", err)
	}
	if len(loadValues) != 3 {
		t.Fatalf("expected three load averages, got %#v", loadValues)
	}
}

func TestCounterDeltaHandlesCounterReset(t *testing.T) {
	if got := counterDelta(120, 100); got != 20 {
		t.Fatalf("expected normal counter delta of 20, got %d", got)
	}
	if got := counterDelta(5, 100); got != 0 {
		t.Fatalf("expected reset counter delta to be clamped to zero, got %d", got)
	}
}

func TestVirtualListenerDialPreservesVisitorAddressAndTransfersBytes(t *testing.T) {
	listener := NewVirtualListener(nil)
	defer listener.Close()

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

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, err := listener.Dial(ctx, "198.51.100.8:43210")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var server net.Conn
	select {
	case server = <-accepted:
	case err := <-acceptErr:
		t.Fatalf("accept virtual connection: %v", err)
	case <-ctx.Done():
		t.Fatal("timed out waiting for virtual connection")
	}
	defer server.Close()
	if got, want := server.RemoteAddr().String(), "198.51.100.8:43210"; got != want {
		t.Fatalf("accepted remote address = %q, want %q", got, want)
	}

	request := make([]byte, 4)
	readRequest := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(server, request)
		readRequest <- err
	}()
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if err := <-readRequest; err != nil {
		t.Fatal(err)
	}
	if string(request) != "ping" {
		t.Fatalf("request = %q, want ping", request)
	}
	response := make([]byte, 4)
	readResponse := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(client, response)
		readResponse <- err
	}()
	if _, err := server.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	if err := <-readResponse; err != nil {
		t.Fatal(err)
	}
	if string(response) != "pong" {
		t.Fatalf("response = %q, want pong", response)
	}
}

func TestVirtualListenerCloseRejectsNewDials(t *testing.T) {
	listener := NewVirtualListener(nil)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := listener.Dial(context.Background(), "198.51.100.8:43210"); err == nil {
		t.Fatal("dial on a closed virtual listener unexpectedly succeeded")
	}
	if _, err := listener.Accept(); err == nil {
		t.Fatal("accept on a closed virtual listener unexpectedly succeeded")
	}
}

func withServerStatus(t *testing.T, status []map[string]interface{}) {
	t.Helper()
	ServerStatusLock.Lock()
	original := ServerStatus
	ServerStatus = status
	ServerStatusLock.Unlock()
	t.Cleanup(func() {
		ServerStatusLock.Lock()
		ServerStatus = original
		ServerStatusLock.Unlock()
	})
}

func assertDashboardStatusFields(t *testing.T, status map[string]interface{}) {
	t.Helper()
	for _, field := range []string{
		"load", "load1", "load5", "load15", "cpu", "swap_mem", "virtual_mem",
		"io_send", "io_recv", "tcp", "udp", "time",
	} {
		if _, ok := status[field]; !ok {
			t.Fatalf("dashboard status is missing %q: %#v", field, status)
		}
	}
}
