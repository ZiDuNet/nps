package server

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"ehang.io/nps/lib/file"
)

type lifecycleTestService struct {
	starts atomic.Int32
	closes atomic.Int32
	err    error
}

func (s *lifecycleTestService) Start() error {
	s.starts.Add(1)
	return s.err
}

func (s *lifecycleTestService) Close() error {
	s.closes.Add(1)
	return nil
}

func TestManagementServiceOwnsHostAndWebLifecycle(t *testing.T) {
	host := &lifecycleTestService{}
	web := &lifecycleTestService{}
	service := &managementService{host: host, web: web}

	if err := service.Start(); err != nil {
		t.Fatalf("start management service: %v", err)
	}
	if host.starts.Load() != 1 || web.starts.Load() != 1 {
		t.Fatalf("expected host and web services to start once, got host=%d web=%d", host.starts.Load(), web.starts.Load())
	}
	if err := service.Close(); err != nil {
		t.Fatalf("close management service: %v", err)
	}
	if host.closes.Load() != 1 || web.closes.Load() != 1 {
		t.Fatalf("expected host and web services to close once, got host=%d web=%d", host.closes.Load(), web.closes.Load())
	}
}

func TestManagementServiceClosesHostWhenWebStartFails(t *testing.T) {
	host := &lifecycleTestService{}
	web := &lifecycleTestService{err: errors.New("web start failed")}
	service := &managementService{host: host, web: web}

	if err := service.Start(); err == nil {
		t.Fatal("expected web startup error")
	}
	if host.closes.Load() != 1 {
		t.Fatalf("host service was not rolled back after web startup failure: %d", host.closes.Load())
	}
}

func TestManagementServiceStartsWebWhenHostStartFails(t *testing.T) {
	host := &lifecycleTestService{err: errors.New("host port unavailable")}
	web := &lifecycleTestService{}
	service := &managementService{host: host, web: web}

	if err := service.Start(); err != nil {
		t.Fatalf("host startup failure must not hide the management panel: %v", err)
	}
	if web.starts.Load() != 1 {
		t.Fatalf("web service starts = %d, want 1", web.starts.Load())
	}
}

func TestStartNewServerRejectsMissingManagementConfiguration(t *testing.T) {
	if err := StartNewServer(0, nil, "tcp", 0); err == nil {
		t.Fatal("missing management configuration must fail startup")
	}
}

func TestManagementServiceKeyIsOutsideTaskNamespace(t *testing.T) {
	var services sync.Map
	services.Store(managementServiceKey, &lifecycleTestService{})
	if _, found := services.Load(0); found {
		t.Fatal("management service must not be reachable through task ID 0")
	}
}

func TestTaskLifecycleRejectsNonPositiveIDs(t *testing.T) {
	for _, id := range []int{-1, 0} {
		t.Run("id", func(t *testing.T) {
			if err := StopServer(id); err == nil {
				t.Fatalf("StopServer(%d) unexpectedly succeeded", id)
			}
			if err := StartTask(id); err == nil {
				t.Fatalf("StartTask(%d) unexpectedly succeeded", id)
			}
			if err := DelTask(id); err == nil {
				t.Fatalf("DelTask(%d) unexpectedly succeeded", id)
			}
			if err := AddTask(&file.Tunnel{Id: id}); err == nil {
				t.Fatalf("AddTask(%d) unexpectedly succeeded", id)
			}
		})
	}
}

func TestRevokeUserClientsWithOnlyTouchesOwnedClients(t *testing.T) {
	var clients sync.Map
	clients.Store(1, &file.Client{Id: 1, UserId: 7})
	clients.Store(2, &file.Client{Id: 2, UserId: 8})
	var disconnected, removed []int
	revokeUserClientsWith(7, &clients, func(id int) { disconnected = append(disconnected, id) }, func(id int, _ bool) { removed = append(removed, id) })
	if len(disconnected) != 1 || disconnected[0] != 1 || len(removed) != 1 || removed[0] != 1 {
		t.Fatalf("revocation touched unexpected clients: disconnected=%v removed=%v", disconnected, removed)
	}
}
