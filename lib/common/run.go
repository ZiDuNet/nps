package common

import (
	"os"
	"path/filepath"
	"runtime"
)

var ConfPath string

// RuntimeTempPath is an explicit application override. Native multi-profile
// hosts set it before startup so npc_vkey.txt never crosses profile boundaries.
// Ordinary CLI, SDK and server behavior is unchanged when it is empty.
var RuntimeTempPath string

// GetRunPath returns the directory holding the running executable and, by
// default, the configuration files. An explicit -conf_path override wins;
// otherwise the configuration always lives next to the binary so that the
// source of conf/nps.conf and conf/instance.key is unambiguous regardless of
// how the process is launched (bare command, PATH lookup or system service).
func GetRunPath() string {
	if ConfPath != "" {
		return ConfPath
	}
	return GetAppPath()
}

// Different systems get different installation paths
func GetInstallPath() string {
	var path string

	if ConfPath != "" {
		return ConfPath
	}

	if IsWindows() {
		path = `C:\Program Files\nps`
	} else {
		path = "/etc/nps"
	}

	return path
}

// GetAppPath returns the directory containing the running executable.
//
// It prefers os.Executable so the result stays correct when the binary is
// launched from PATH (os.Args[0] may be just the bare command name) or by a
// system service whose working directory is unrelated to the binary location.
// The os.Args[0] branch is kept as a fallback for platforms or sandboxes where
// the real executable path is unavailable (for example /proc not mounted on
// Linux).
func GetAppPath() string {
	if exe, err := os.Executable(); err == nil {
		if abs, err := filepath.Abs(filepath.Dir(exe)); err == nil {
			return abs
		}
		return filepath.Dir(exe)
	}
	if path, err := filepath.Abs(filepath.Dir(os.Args[0])); err == nil {
		return path
	}
	return os.Args[0]
}

// Determine whether the current system is a Windows system?
func IsWindows() bool {
	if runtime.GOOS == "windows" {
		return true
	}
	return false
}

// interface log file path
func GetLogPath() string {
	var path string
	if IsWindows() {
		path = filepath.Join(GetAppPath(), "nps.log")
	} else {
		path = "/var/log/nps.log"
	}
	return path
}

func GetLogPathCurrentPath() string {
	var path string
	path = filepath.Join(GetAppPath(), "nps.log")
	return path
}

// interface npc log file path
func GetNpcLogPath() string {
	var path string
	if IsWindows() {
		path = filepath.Join(GetAppPath(), "npc.log")
	} else {
		path = "/var/log/npc.log"
	}
	return path
}

// interface pid file path
func GetTmpPath() string {
	if RuntimeTempPath != "" {
		return RuntimeTempPath
	}
	var path string
	if IsWindows() {
		path = GetAppPath()
	} else {
		path = "/tmp"
	}
	return path
}

// config file path
func GetConfigPath() string {
	var path string
	if IsWindows() {
		path = filepath.Join(GetAppPath(), "conf/npc.conf")
	} else {
		path = "conf/npc.conf"
	}
	return path
}
