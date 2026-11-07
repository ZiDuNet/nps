package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/install"
)

func guiUpdateReceiptPath() string {
	return filepath.Join(filepath.Dir(getStoragePath()), "update-result.json")
}
func guiResumePath() string {
	return filepath.Join(filepath.Dir(getStoragePath()), "update-resume.json")
}

func readGUIUpdateReceipt() *common.ClientUpdateProgress {
	data, err := os.ReadFile(guiUpdateReceiptPath())
	if err != nil {
		return nil
	}
	var result common.ClientUpdateProgress
	if json.Unmarshal(data, &result) != nil {
		return nil
	}
	return &result
}

func (a *App) restartAppLocked(beforeQuit func()) error {
	if a.pendingUpdate == nil || a.pendingUpdateDir == "" {
		return errors.New("没有已校验的更新包，请重新检查更新")
	}
	if a.app == nil {
		return errors.New("GUI 应用尚未初始化")
	}
	if err := saveUpdateConnections(); err != nil {
		return err
	}
	planPath, err := install.StartUpdateHelper(a.pendingUpdateDir, *a.pendingUpdate)
	if err != nil {
		_ = os.Remove(guiResumePath())
		return err
	}
	beforeQuit()
	if err = install.ReleaseUpdateHelper(planPath); err != nil {
		_ = os.Remove(guiResumePath())
		return err
	}
	go func() { time.Sleep(200 * time.Millisecond); setQuitting(); a.app.Quit() }()
	return nil
}

type guiResume struct {
	Created time.Time `json:"created"`
	IDs     []string  `json:"ids"`
}

func saveUpdateConnections() error {
	runningMu.Lock()
	ids := make([]string, 0, len(running))
	for id := range running {
		ids = append(ids, id)
	}
	runningMu.Unlock()
	sort.Strings(ids)
	data, err := json.Marshal(guiResume{Created: time.Now(), IDs: ids})
	if err != nil {
		return err
	}
	return writeGUIStateAtomic(guiResumePath(), data)
}

func writeGUIStateAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".npc-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = install.SecureUpdateFile(f.Name()); err != nil {
		_ = f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Restore in Go, independently of the WebView and RememberClientState. A
// remote upgrade must resume connecting/retrying clients, but never start an
// entry that was stopped before the upgrade.
func (a *App) restoreUpdateConnections() error {
	return restoreGUIUpdateConnections(func(id string) error { return a.ToggleClient(id, "", "", false, true) })
}

func restoreGUIUpdateConnections(start func(string) error) error {
	data, err := os.ReadFile(guiResumePath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var resume guiResume
	if err = json.Unmarshal(data, &resume); err != nil {
		return err
	}
	if time.Since(resume.Created) > 24*time.Hour {
		_ = os.Remove(guiResumePath())
		return nil
	}
	loadShortcuts()
	shortcutsMu.Lock()
	store, err := loadPersistentStore()
	if err == nil {
		available := make(map[string]bool, len(store.Shortcuts))
		for _, shortcut := range store.Shortcuts {
			available[shortcutID(shortcut)] = true
		}
		ids := resume.IDs[:0]
		for _, id := range resume.IDs {
			if available[id] {
				ids = append(ids, id)
			}
		}
		resume.IDs = ids
		store.ClientStates = make(map[string]string)
		for _, id := range resume.IDs {
			store.ClientStates[id] = "connecting"
		}
		var encoded []byte
		encoded, err = json.MarshalIndent(store, "", "  ")
		if err == nil {
			err = writeGUIStateAtomic(getStoragePath(), encoded)
		}
	}
	shortcutsMu.Unlock()
	if err != nil {
		return err
	}
	for _, id := range resume.IDs {
		if err = start(id); err != nil {
			return err
		}
	}
	path := guiResumePath()
	// Keep the snapshot through the helper's short startup-health window so
	// rollback can reconnect exactly the same running entries.
	go func() { time.Sleep(30 * time.Second); _ = os.Remove(path) }()
	return nil
}

func extractGUIExecutable(archive, destination, currentName string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		name := filepath.Base(f.Name)
		if f.FileInfo().IsDir() || !(strings.EqualFold(name, currentName) || strings.EqualFold(name, "npc-gui") || strings.EqualFold(name, "npc-gui.exe")) {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0700)
		if err != nil {
			r.Close()
			return err
		}
		_, err = io.Copy(w, r)
		closeErr := w.Close()
		r.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	return errors.New("更新包缺少 GUI 程序")
}
