//go:build !sdk

package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ehang.io/nps/lib/quickcommand"
	"github.com/kardianos/service"
)

func TestManualUpdateRestartsOnlyApprovedRunningServices(t *testing.T) {
	for _, restart := range []bool{false, true} {
		dir := t.TempDir()
		source := filepath.Join(dir, "new")
		target := filepath.Join(dir, "npc")
		_ = os.WriteFile(source, []byte("new"), 0755)
		_ = os.WriteFile(target, []byte("old"), 0755)
		var actions []string
		services := []npcUpdateService{{Name: "nps-client-a", Status: service.StatusRunning}, {Name: "nps-client-b", Status: service.StatusStopped}}
		err := applyManualNpcUpdate(source, target, services, restart, func(name, action string) error { actions = append(actions, name+":"+action); return nil })
		if err != nil {
			t.Fatal(err)
		}
		if restart && strings.Join(actions, ",") != "nps-client-a:restart" || !restart && len(actions) != 0 {
			t.Fatalf("restart=%v actions=%v", restart, actions)
		}
	}
}

func TestManualUpdateRollsBackOnRestartFailure(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "new")
	target := filepath.Join(dir, "npc")
	_ = os.WriteFile(source, []byte("new"), 0755)
	_ = os.WriteFile(target, []byte("old"), 0755)
	count := 0
	err := applyManualNpcUpdate(source, target, []npcUpdateService{{Name: "nps-client-a", Status: service.StatusRunning}}, true, func(_, _ string) error {
		count++
		if count == 1 {
			return errors.New("start failed")
		}
		return nil
	})
	if err == nil || count != 2 {
		t.Fatalf("err=%v control count=%d", err, count)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "old" {
		t.Fatal("failed restart did not roll back")
	}
}

func TestRuntimeMenuConnectionsSurviveUpgrade(t *testing.T) {
	runtimeCommands.Lock()
	previous := runtimeCommands.items
	runtimeCommands.items = nil
	runtimeCommands.Unlock()
	args := os.Args
	t.Cleanup(func() {
		runtimeCommands.Lock()
		runtimeCommands.items = previous
		runtimeCommands.Unlock()
		os.Args = args
	})
	os.Args = []string{"npc", "-debug=false", "-resume_commands=stale.json"}
	rememberRuntimeCommand(quickcommand.Command{Server: "bridge.test:8027", VKey: "test-key", Transport: "ws", BridgePath: "/bridge"})
	rememberRuntimeCommand(quickcommand.Command{Server: "bridge.test:8024", VKey: "other-key", Transport: "tcp"})
	rememberRuntimeCommand(quickcommand.Command{Server: "bridge.test:8027", VKey: "test-key", Transport: "ws", BridgePath: "/other-bridge"})
	rememberRuntimeCommand(quickcommand.Command{Name: "same connection", Server: "bridge.test:8027", VKey: "test-key", Transport: "ws", BridgePath: "/bridge"})
	restart, err := npcRestartArgs()
	if err != nil {
		t.Fatal(err)
	}
	if len(restart) != 2 || restart[0] != "-debug=false" || strings.Contains(restart[1], "stale.json") {
		t.Fatalf("restart args=%v", restart)
	}
	path := strings.TrimPrefix(restart[1], "-resume_commands=")
	defer os.Remove(path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatal("connection restore file exposes credentials")
	}
	commands, err := readResumeCommands(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 3 || commands[0].Transport != "ws" || commands[0].BridgePath != "/bridge" || commands[1].VKey != "other-key" || commands[2].BridgePath != "/other-bridge" {
		t.Fatalf("commands=%+v", commands)
	}
}
