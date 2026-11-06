package controllers

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/lib/common"
	"github.com/astaxie/beego"
)

const (
	webSessionRegistryVersion = 1
	webSessionDeviceIDBytes   = 18
	webSessionRegistryLimit   = 10000
)

// webSessionDeviceRecord is the server-side state behind a remember-login
// cookie. The token itself remains signed and contains no reusable credential;
// this record makes individual listing and revocation possible.
type webSessionDeviceRecord struct {
	ID         string `json:"id"`
	Principal  string `json:"principal"`
	Username   string `json:"username"`
	UserID     int    `json:"user_id,omitempty"`
	ClientID   int    `json:"client_id,omitempty"`
	IP         string `json:"ip,omitempty"`
	UserAgent  string `json:"user_agent,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	LastSeenAt int64  `json:"last_seen_at"`
	ExpiresAt  int64  `json:"expires_at"`
	Epoch      uint64 `json:"epoch"`
}

type webSessionRegistryFile struct {
	Version int                               `json:"version"`
	Epochs  map[string]uint64                 `json:"epochs,omitempty"`
	Devices map[string]webSessionDeviceRecord `json:"devices,omitempty"`
}

type webSessionRegistry struct {
	mu        sync.Mutex
	path      string
	loaded    bool
	version   int
	epochs    map[string]uint64
	devices   map[string]webSessionDeviceRecord
	loadError error
}

func defaultWebSessionRegistryPath() string {
	return filepath.Join(common.GetRunPath(), "conf", "web_sessions.json")
}

var webSessionRegistryPath = defaultWebSessionRegistryPath

var rememberLoginDevices = newWebSessionRegistry(webSessionRegistryPath)

func newWebSessionRegistry(path func() string) *webSessionRegistry {
	registryPath := ""
	if path != nil {
		registryPath = path()
	}
	return &webSessionRegistry{path: registryPath, epochs: make(map[string]uint64), devices: make(map[string]webSessionDeviceRecord)}
}

func (s *webSessionRegistry) resetForTests(path string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.path = path
	s.loaded = true
	s.version = webSessionRegistryVersion
	s.epochs = make(map[string]uint64)
	s.devices = make(map[string]webSessionDeviceRecord)
	s.loadError = nil
}

func (s *webSessionRegistry) ensureLoadedLocked() error {
	if s == nil {
		return errors.New("web session registry is unavailable")
	}
	if s.loaded {
		return s.loadError
	}
	s.loaded = true
	s.version = webSessionRegistryVersion
	s.epochs = make(map[string]uint64)
	s.devices = make(map[string]webSessionDeviceRecord)
	if s.path == "" {
		if webSessionRegistryPath == nil {
			return errors.New("web session registry path is unavailable")
		}
		s.path = webSessionRegistryPath()
	}
	content, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		s.loadError = err
		return err
	}
	var stored webSessionRegistryFile
	if err := json.Unmarshal(content, &stored); err != nil {
		s.loadError = err
		return err
	}
	if stored.Version != 0 && stored.Version != webSessionRegistryVersion {
		s.loadError = fmt.Errorf("unsupported web session registry version %d", stored.Version)
		return s.loadError
	}
	s.version = webSessionRegistryVersion
	if stored.Epochs != nil {
		s.epochs = stored.Epochs
	}
	if stored.Devices != nil {
		s.devices = stored.Devices
	}
	return nil
}

func (s *webSessionRegistry) saveLocked() error {
	if s.path == "" {
		return errors.New("web session registry path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	stored := webSessionRegistryFile{
		Version: webSessionRegistryVersion,
		Epochs:  s.epochs,
		Devices: s.devices,
	}
	content, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".web-sessions-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.path)
}

func (s *webSessionRegistry) cleanupLocked(now time.Time) bool {
	changed := false
	for id, record := range s.devices {
		if record.ExpiresAt <= now.Unix() {
			delete(s.devices, id)
			changed = true
		}
	}
	if len(s.devices) > webSessionRegistryLimit {
		records := make([]webSessionDeviceRecord, 0, len(s.devices))
		for _, record := range s.devices {
			records = append(records, record)
		}
		sort.Slice(records, func(i, j int) bool {
			return records[i].ExpiresAt > records[j].ExpiresAt
		})
		s.devices = make(map[string]webSessionDeviceRecord, webSessionRegistryLimit)
		for _, record := range records[:webSessionRegistryLimit] {
			s.devices[record.ID] = record
		}
		changed = true
	}
	return changed
}

func (s *webSessionRegistry) Epoch(principal string) uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return 0
	}
	return s.epochs[principal]
}

func (s *webSessionRegistry) SessionEpochValid(principal string, epoch uint64) bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		beego.Warn("web session registry:", err)
		return false
	}
	return epoch == s.epochs[principal]
}

func (s *webSessionRegistry) Register(record webSessionDeviceRecord) error {
	if s == nil {
		return errors.New("web session registry is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return err
	}
	s.cleanupLocked(time.Now())
	if len(s.devices) >= webSessionRegistryLimit {
		return errors.New("web session device limit reached")
	}
	s.devices[record.ID] = record
	return s.saveLocked()
}

func (s *webSessionRegistry) Validate(id, principal string, epoch uint64, now time.Time) bool {
	if s == nil || id == "" {
		return epoch == 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return false
	}
	record, ok := s.devices[id]
	if !ok || record.Principal != principal || record.Epoch != epoch || record.ExpiresAt <= now.Unix() {
		if ok {
			delete(s.devices, id)
			_ = s.saveLocked()
		}
		return false
	}
	if now.Unix()-record.LastSeenAt >= 300 {
		record.LastSeenAt = now.Unix()
		s.devices[id] = record
		_ = s.saveLocked()
	}
	return true
}

func (s *webSessionRegistry) List(principal string, now time.Time) ([]webSessionDeviceRecord, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return nil, err
	}
	changed := s.cleanupLocked(now)
	records := make([]webSessionDeviceRecord, 0)
	for _, record := range s.devices {
		if record.Principal == principal {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].LastSeenAt != records[j].LastSeenAt {
			return records[i].LastSeenAt > records[j].LastSeenAt
		}
		return records[i].CreatedAt > records[j].CreatedAt
	})
	if changed {
		_ = s.saveLocked()
	}
	return records, nil
}

func (s *webSessionRegistry) Revoke(principal, id string) (bool, error) {
	if s == nil || strings.TrimSpace(id) == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return false, err
	}
	record, ok := s.devices[id]
	if !ok || record.Principal != principal {
		return false, nil
	}
	delete(s.devices, id)
	return true, s.saveLocked()
}

func (s *webSessionRegistry) RevokeAll(principal string, now time.Time) (int, uint64, error) {
	if s == nil {
		return 0, 0, errors.New("web session registry is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return 0, 0, err
	}
	s.cleanupLocked(now)
	count := 0
	for id, record := range s.devices {
		if record.Principal == principal {
			delete(s.devices, id)
			count++
		}
	}
	s.epochs[principal]++
	if s.epochs[principal] == 0 {
		s.epochs[principal] = 1
	}
	epoch := s.epochs[principal]
	return count, epoch, s.saveLocked()
}

func newWebSessionDeviceID() (string, error) {
	raw := make([]byte, webSessionDeviceIDBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
