package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/version"
)

type localUpdateReceipt struct {
	common.ClientUpdateProgress
	Backup         string `json:"rollback_backup,omitempty"`
	StartedPID     int    `json:"started_pid,omitempty"`
	RunningVersion string `json:"running_version,omitempty"`
}

func WriteExecUpdateReceipt(path string, result common.ClientUpdateProgress, backup string) error {
	data, err := json.Marshal(localUpdateReceipt{ClientUpdateProgress: result, Backup: backup})
	if err != nil {
		return err
	}
	return writeUpdateFile(path, data)
}

// Only the matching new executable may retire the exact adjacent backup made
// by its transaction. Receipt contents never authorize arbitrary deletion.
func FinalizeExecUpdateReceipt(path, target, runningVersion string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var receipt localUpdateReceipt
	if json.Unmarshal(data, &receipt) != nil || receipt.Stage != "restarted" {
		return
	}
	cmp, err := version.Compare(receipt.Version, runningVersion)
	if err != nil || cmp != 0 || receipt.Backup == "" {
		return
	}
	if filepath.Dir(receipt.Backup) != filepath.Dir(target) || !strings.HasPrefix(filepath.Base(receipt.Backup), ".npc-rollback-") {
		return
	}
	info, err := os.Lstat(receipt.Backup)
	if err == nil && info.Mode().IsRegular() {
		_ = os.Remove(receipt.Backup)
	}
}

// GUI startup is acknowledged after its backend and saved connections have
// initialized. In particular, macOS `open` exiting zero is not evidence that
// the application actually started.
func AcknowledgeUpdateStartup(path, runningVersion string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var receipt localUpdateReceipt
	if json.Unmarshal(data, &receipt) != nil {
		return
	}
	receipt.StartedPID = os.Getpid()
	receipt.RunningVersion = runningVersion
	data, err = json.Marshal(receipt)
	if err == nil {
		_ = writeUpdateFile(path, data)
	}
}

func updateStartupConfirmed(path, expected string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var receipt localUpdateReceipt
	if json.Unmarshal(data, &receipt) != nil || receipt.StartedPID <= 0 || !ProcessRunning(receipt.StartedPID) {
		return false
	}
	if expected == "" {
		return true
	}
	cmp, err := version.Compare(receipt.RunningVersion, expected)
	return err == nil && cmp == 0
}
