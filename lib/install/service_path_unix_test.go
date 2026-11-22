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

func TestSystemdCommandAccumulatesEnvironmentValues(t *testing.T) {
	command, err := parseSystemdCommand(`{ path=/opt/nps ; argv[]=/opt/nps service -conf_path=/srv/nps ; }
Environment=NPS_WEB_IP=0.0.0.0
Environment=NPS_WEB_BASE_URL=/nps
Environment=IGNORED_WITHOUT_VALUE`)
	if err != nil {
		t.Fatal(err)
	}
	if command.ConfigPath != "/srv/nps" || len(command.Environment) != 3 ||
		command.Environment[0] != "NPS_WEB_IP=0.0.0.0" ||
		command.Environment[1] != "NPS_WEB_BASE_URL=/nps" ||
		command.Environment[2] != "IGNORED_WITHOUT_VALUE" {
		t.Fatalf("unexpected systemd command: %#v", command)
	}
}

func TestSystemdShowSeparatesExecStartAndEnvironmentProperties(t *testing.T) {
	command, err := parseSystemdShowOutput("ExecStart={ path=/opt/nps ; argv[]=/opt/nps service -conf_path=/srv/nps ; }\nEnvironment=NPS_WEB_IP=0.0.0.0 NPS_WEB_BASE_URL=/nps\nEnvironment=IGNORED_WITHOUT_VALUE")
	if err != nil {
		t.Fatal(err)
	}
	if command.Executable != "/opt/nps" || command.ConfigPath != "/srv/nps" {
		t.Fatalf("unexpected command: %#v", command)
	}
	if len(command.Environment) != 3 ||
		command.Environment[0] != "NPS_WEB_IP=0.0.0.0" ||
		command.Environment[1] != "NPS_WEB_BASE_URL=/nps" ||
		command.Environment[2] != "IGNORED_WITHOUT_VALUE" {
		t.Fatalf("unexpected environment: %#v", command.Environment)
	}
}

func TestSystemdShowPreservesQuotedEnvironmentValue(t *testing.T) {
	command, err := parseSystemdShowOutput("ExecStart={ path=/opt/nps ; argv[]=/opt/nps service ; }\nEnvironment=\"NPS_SERVICE_NAME=NPS Admin\" NPS_WEB_IP=127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(command.Environment) != 2 ||
		command.Environment[0] != "NPS_SERVICE_NAME=NPS Admin" ||
		command.Environment[1] != "NPS_WEB_IP=127.0.0.1" {
		t.Fatalf("unexpected environment: %#v", command.Environment)
	}
}

func TestSystemdCommandParsesSeparatedConfigPath(t *testing.T) {
	command, err := parseSystemdCommand(`{ path=/opt/nps ; argv[]=/opt/nps service -conf_path /srv/nps ; }`)
	if err != nil {
		t.Fatal(err)
	}
	if command.ConfigPath != "/srv/nps" {
		t.Fatalf("ConfigPath = %q", command.ConfigPath)
	}
}

func TestSystemdCommandHandlesSpacedExecutableWithArguments(t *testing.T) {
	command, err := parseSystemdCommand(`{ path=/home/test/NPS Client/nps ; argv[]=/home/test/NPS Client/nps service -conf_path "/srv/NPS Data" ; }`)
	if err != nil {
		t.Fatal(err)
	}
	if command.Executable != "/home/test/NPS Client/nps" || command.ConfigPath != "/srv/NPS Data" {
		t.Fatalf("unexpected command: %#v", command)
	}
}
