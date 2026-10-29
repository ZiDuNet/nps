//go:build !sdk
// +build !sdk

package main

import (
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"ehang.io/nps/lib/quickcommand"
	"github.com/kardianos/service"
)

func TestNpcConnectionGuardCountsLoopsIndependently(t *testing.T) {
	serviceConnGuard.Lock()
	serviceConnGuard.conns = 0
	serviceConnGuard.Unlock()
	t.Cleanup(func() {
		serviceConnGuard.Lock()
		serviceConnGuard.conns = 0
		serviceConnGuard.Unlock()
	})

	registerNpcConn()
	registerNpcConn()
	registerNpcConn()

	// A kicked loop must not uninstall while other loops are still alive.
	if popNpcConn() {
		t.Fatal("a middle loop reported itself as the last connection")
	}
	if popNpcConn() {
		t.Fatal("a middle loop reported itself as the last connection")
	}
	if !popNpcConn() {
		t.Fatal("the final loop did not report itself as the last connection")
	}
}

func TestNpcConnectionGuardIsSafeUnderConcurrentRegistration(t *testing.T) {
	serviceConnGuard.Lock()
	serviceConnGuard.conns = 0
	serviceConnGuard.Unlock()
	t.Cleanup(func() {
		serviceConnGuard.Lock()
		serviceConnGuard.conns = 0
		serviceConnGuard.Unlock()
	})

	const loops = 32
	var wg sync.WaitGroup
	for i := 0; i < loops; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			registerNpcConn()
		}()
	}
	wg.Wait()

	lastObserved := make(chan bool, loops)
	for i := 0; i < loops; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lastObserved <- popNpcConn()
		}()
	}
	wg.Wait()
	close(lastObserved)

	lastCount := 0
	for last := range lastObserved {
		if last {
			lastCount++
		}
	}
	if lastCount != 1 {
		t.Fatalf("exactly one loop must be reported as last, got %d", lastCount)
	}
}

func TestExpandNpcConnectionsAlignsMultiServerArguments(t *testing.T) {
	oldTLS := *tlsEnable
	*tlsEnable = false
	t.Cleanup(func() { *tlsEnable = oldTLS })
	connections := expandNpcConnections("a:1,b:2", "key-a,key-b", "tcp,tls", "192.0.2.10")
	if len(connections) != 2 {
		t.Fatalf("connection count = %d, want 2", len(connections))
	}
	if connections[0].server != "a:1" || connections[0].vkey != "key-a" || connections[0].transport != "tcp" || connections[0].tls {
		t.Fatalf("first connection = %+v", connections[0])
	}
	if connections[1].server != "b:2" || connections[1].vkey != "key-b" || connections[1].transport != "tcp" || !connections[1].tls || connections[1].localIP != "192.0.2.10" {
		t.Fatalf("second connection = %+v", connections[1])
	}
}

func TestExpandNpcConnectionsReusesLastValuesAndRejectsMissingCredentials(t *testing.T) {
	connections := expandNpcConnections("a:1,b:2,c:3", "key", "", "")
	if len(connections) != 3 {
		t.Fatalf("connection count = %d, want 3", len(connections))
	}
	for _, connection := range connections {
		if connection.vkey != "key" || connection.transport != "tcp" {
			t.Fatalf("connection did not reuse defaults: %+v", connection)
		}
	}
	if got := expandNpcConnections("a:1", "", "tcp", ""); got != nil {
		t.Fatalf("missing vkey produced %#v, want nil", got)
	}
}

func TestServiceStatusText(t *testing.T) {
	tests := []struct {
		name   string
		status service.Status
		err    error
		want   string
	}{
		{name: "running", status: service.StatusRunning, want: "运行中"},
		{name: "stopped", status: service.StatusStopped, want: "已停止"},
		{name: "unknown", status: service.StatusUnknown, want: "未知"},
		{name: "error", err: errors.New("not installed"), want: "未安装或状态未知"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := serviceStatusText(tt.status, tt.err); got != tt.want {
				t.Fatalf("serviceStatusText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewNpcServiceConfig(t *testing.T) {
	config := newNpcServiceConfig("nps-client-test", "test", "description")
	if config.Name != "nps-client-test" {
		t.Fatalf("service name = %q, want %q", config.Name, "nps-client-test")
	}
	if config.Option == nil {
		t.Fatal("service options must be initialized")
	}
	if runtime.GOOS == "windows" {
		return
	}
	if config.Option["SystemdScript"] == nil || config.Option["SysvScript"] == nil {
		t.Fatal("POSIX service scripts must be configured")
	}
	if len(config.Dependencies) != 2 {
		t.Fatalf("service dependencies = %v, want network dependencies", config.Dependencies)
	}
}

func TestFindNpcCommandAfterFlags(t *testing.T) {
	command, index := findNpcCommand([]string{"-service_name=nps-client-key", "restart"})
	if command != "restart" || index != 1 {
		t.Fatalf("findNpcCommand() = (%q, %d), want (restart, 1)", command, index)
	}
	command, index = findNpcCommand([]string{"-service_name=nps-client-key", "update-force"})
	if command != "update-force" || index != 1 {
		t.Fatalf("findNpcCommand() = (%q, %d), want (update-force, 1)", command, index)
	}
}

func TestResolveNpcServiceName(t *testing.T) {
	names := []string{"nps-client-alpha", "nps-client-beta", "nps-client-gamma-ws"}
	tests := []struct {
		input string
		want  string
		fail  bool
	}{
		{input: "alpha", want: "nps-client-alpha"},
		{input: "nps-client-beta", want: "nps-client-beta"},
		{input: "gamma", want: "nps-client-gamma-ws"},
		{input: "missing", fail: true},
		{input: "../alpha", fail: true},
		{input: "", fail: true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := resolveNpcServiceName(names, tt.input)
			if tt.fail {
				if err == nil {
					t.Fatalf("resolveNpcServiceName(%q) unexpectedly succeeded with %q", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveNpcServiceName(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("resolveNpcServiceName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNpcServiceNameSeparatesProtocols(t *testing.T) {
	tests := []struct {
		command quickcommand.Command
		want    string
	}{
		{command: quickcommand.Command{Server: "bridge:8024", VKey: "alpha", Transport: "tcp"}, want: "nps-client-alpha"},
		{command: quickcommand.Command{Server: "bridge:8025", VKey: "alpha", Transport: "tcp", TLS: true}, want: "nps-client-alpha-tls"},
		{command: quickcommand.Command{Server: "bridge:8027", VKey: "alpha", Transport: "ws", BridgePath: "/ws"}, want: "nps-client-alpha-ws"},
		{command: quickcommand.Command{Server: "bridge:8028", VKey: "alpha", Transport: "wss", BridgePath: "/ws"}, want: "nps-client-alpha-wss"},
		{command: quickcommand.Command{Server: "bridge:8029", VKey: "alpha", Transport: "quic", QuicALPN: "nps"}, want: "nps-client-alpha-quic"},
		{command: quickcommand.Command{Server: "bridge:8443", VKey: "alpha", Endpoint: "http3", Transport: "quic", QuicALPN: "nps"}, want: "nps-client-alpha-http3"},
	}
	for _, tt := range tests {
		if got := npcServiceName(tt.command); got != tt.want {
			t.Fatalf("npcServiceName(%+v) = %q, want %q", tt.command, got, tt.want)
		}
	}
}

func TestResolveNpcServiceNameRejectsAmbiguousProtocolServices(t *testing.T) {
	_, err := resolveNpcServiceName([]string{
		"nps-client-alpha-tls",
		"nps-client-alpha-ws",
	}, "alpha")
	if err == nil {
		t.Fatal("ambiguous protocol services should require a complete service name")
	}
}

func TestNpcServiceLogPath(t *testing.T) {
	path, err := npcServiceLogPath("nps-client-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := filepath.Base(path), "npc-alpha.log"; got != want {
		t.Fatalf("log filename = %q, want %q", got, want)
	}
	if _, err := npcServiceLogPath("nps-client-../outside"); err == nil {
		t.Fatal("expected invalid service name to be rejected")
	}
}
