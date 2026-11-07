//go:build !windows

package install

import "testing"

func TestRegisteredServicePathParsing(t *testing.T) {
	path, err := systemdExecutable("{ path=/home/test/Desktop/NPS client/npc ; argv[]=/home/test/Desktop/NPS client/npc -vkey=hidden ; }")
	if err != nil || path != "/home/test/Desktop/NPS client/npc" {
		t.Fatalf("path=%q err=%v", path, err)
	}
	path, err = sysvExecutable("#!/bin/sh\ncmd=\"/usr/local/bin/npc -server=example:8024\"\n")
	if err != nil || path != "/usr/local/bin/npc" {
		t.Fatalf("path=%q err=%v", path, err)
	}
	path, err = plistExecutable([]byte(`<plist><dict><key>ProgramArguments</key><array><string>/Applications/NPS Client/npc</string><string>-vkey=x</string></array></dict></plist>`))
	if err != nil || path != "/Applications/NPS Client/npc" {
		t.Fatalf("path=%q err=%v", path, err)
	}
	if _, err = systemdExecutable("not a path"); err == nil {
		t.Fatal("ambiguous path accepted")
	}
	if _, err = sysvExecutable("cmd=\"$OTHER/npc\""); err == nil {
		t.Fatal("shell expansion accepted")
	}
}
