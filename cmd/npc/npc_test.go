//go:build !sdk
// +build !sdk

package main

import (
	"errors"
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
