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

// Get the currently selected configuration file directory
// For non-Windows systems, select the /etc/nps as config directory if exist, or select ./
// windows system, select the C:\Program Files\nps as config directory if exist, or select ./
func GetRunPath() string {
	if ConfPath != "" {
		return ConfPath
	}

	var path string
	if len(os.Args) == 1 {
		if !IsWindows() {
			dir, _ := filepath.Abs(filepath.Dir(os.Args[0])) //返回
			return dir + "/"
		} else {
			return "./"
		}
	} else {
		if path = GetInstallPath(); !FileExists(path) {
			return GetAppPath()
		}
	}
	return path
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
