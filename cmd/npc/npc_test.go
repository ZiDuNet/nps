//go:build !sdk
// +build !sdk

package main

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kardianos/service"
)

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
}

func TestResolveNpcServiceName(t *testing.T) {
	names := []string{"nps-client-alpha", "nps-client-beta"}
	tests := []struct {
		input string
		want  string
		fail  bool
	}{
		{input: "alpha", want: "nps-client-alpha"},
		{input: "nps-client-beta", want: "nps-client-beta"},
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
