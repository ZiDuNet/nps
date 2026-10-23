package main

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/astaxie/beego/logs"
)

const rotatingLogAdapter = "nps-rotating-file"

var rotatedLogName = regexp.MustCompile(`^(.+)\.[0-9]{8}-[0-9]{6}\.[0-9]{9}\.[0-9]{6}(\.gz)?$`)

type rotatingLogConfig struct {
	Filename    string `json:"filename"`
	MaxSizeMB   int64  `json:"maxsize_mb"`
	MaxAgeDays  int    `json:"maxage_days"`
	MaxBackups  int    `json:"maxbackups"`
	Compress    bool   `json:"compress"`
	RotateDaily bool   `json:"daily"`
	Level       int    `json:"level"`
	Perm        uint32 `json:"perm"`
}

type rotatingFileLogger struct {
	mu        sync.Mutex
	config    rotatingLogConfig
	file      *os.File
	size      int64
	openedDay string
	sequence  uint64
	archives  chan string
	workerWG  sync.WaitGroup
	closed    bool
}

func init() {
	logs.Register(rotatingLogAdapter, func() logs.Logger { return &rotatingFileLogger{} })
}

func (w *rotatingFileLogger) Init(raw string) error {
	w.config = rotatingLogConfig{
		MaxSizeMB:   100,
		MaxAgeDays:  30,
		MaxBackups:  10,
		Compress:    true,
		RotateDaily: true,
		Level:       logs.LevelDebug,
		Perm:        0640,
	}
	if err := json.Unmarshal([]byte(raw), &w.config); err != nil {
		return fmt.Errorf("parse rotating log config: %w", err)
	}
	if strings.TrimSpace(w.config.Filename) == "" {
		return errors.New("rotating log filename is required")
	}
	if w.config.MaxSizeMB < 0 || w.config.MaxAgeDays < 0 || w.config.MaxBackups < 0 {
		return errors.New("rotating log size, age, and backup limits cannot be negative")
	}
	if w.config.MaxSizeMB > int64(^uint64(0)>>1)/(1024*1024) {
		return errors.New("rotating log size is too large")
	}
	if w.config.Perm == 0 {
		w.config.Perm = 0640
	}
	if err := os.MkdirAll(filepath.Dir(w.config.Filename), 0750); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	if err := w.openActiveFile(); err != nil {
		return err
	}
	w.archives = make(chan string, 8)
	w.workerWG.Add(1)
	go w.processArchives()
	return nil
}

func (w *rotatingFileLogger) openActiveFile() error {
	f, err := os.OpenFile(w.config.Filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, os.FileMode(w.config.Perm))
	if err != nil {
		return fmt.Errorf("open log file %q: %w", w.config.Filename, err)
	}
	if err := f.Chmod(os.FileMode(w.config.Perm)); err != nil {
		_ = f.Close()
		return fmt.Errorf("set log file permissions: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	w.file = f
	w.size = info.Size()
	w.openedDay = info.ModTime().Local().Format("2006-01-02")
	return nil
}

func (w *rotatingFileLogger) WriteMsg(when time.Time, msg string, level int) error {
	if level > w.config.Level {
		return nil
	}
	line := []byte(when.Format("2006/01/02 15:04:05.000 ") + msg + "\n")
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.file == nil {
		if w.closed {
			return errors.New("rotating log writer is closed")
		}
		if err := w.openActiveFile(); err != nil {
			return err
		}
	}
	day := when.Local().Format("2006-01-02")
	maxSize := w.config.MaxSizeMB * 1024 * 1024
	if (w.config.RotateDaily && day != w.openedDay) || (maxSize > 0 && w.size > 0 && w.size+int64(len(line)) > maxSize) {
		if err := w.rotate(when); err != nil {
			return err
		}
	}
	n, err := w.file.Write(line)
	w.size += int64(n)
	return err
}

func (w *rotatingFileLogger) rotate(when time.Time) error {
	if err := w.file.Close(); err != nil {
		w.file = nil
		if reopenErr := w.openActiveFile(); reopenErr != nil {
			return errors.Join(fmt.Errorf("close active log before rotation: %w", err), reopenErr)
		}
		return fmt.Errorf("close active log before rotation: %w", err)
	}
	w.file = nil
	var archivePath string
	for {
		w.sequence++
		archivePath = fmt.Sprintf("%s.%s.%06d", w.config.Filename, when.Local().Format("20060102-150405.000000000"), w.sequence)
		if _, err := os.Lstat(archivePath); errors.Is(err, os.ErrNotExist) {
			break
		} else if err != nil {
			if reopenErr := w.openActiveFile(); reopenErr != nil {
				return errors.Join(fmt.Errorf("check rotated log path: %w", err), reopenErr)
			}
			return fmt.Errorf("check rotated log path: %w", err)
		}
	}
	if err := os.Rename(w.config.Filename, archivePath); err != nil {
		if reopenErr := w.openActiveFile(); reopenErr != nil {
			return errors.Join(fmt.Errorf("rename active log: %w", err), reopenErr)
		}
		return fmt.Errorf("rename active log: %w", err)
	}
	if err := w.openActiveFile(); err != nil {
		w.archives <- archivePath
		return err
	}
	w.openedDay = when.Local().Format("2006-01-02")
	w.archives <- archivePath
	return nil
}

func (w *rotatingFileLogger) processArchives() {
	defer w.workerWG.Done()
	for path := range w.archives {
		if w.config.Compress {
			if err := compressLogFile(path); err != nil {
				fmt.Fprintf(os.Stderr, "NPS log archive compression failed for %q: %v\n", path, err)
			}
		}
		if err := w.pruneArchives(time.Now()); err != nil {
			fmt.Fprintf(os.Stderr, "NPS log archive retention failed: %v\n", err)
		}
	}
}

func compressLogFile(path string) error {
	input, err := os.Open(path)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".nps-log-*.gz.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	zipWriter := gzip.NewWriter(tmp)
	if _, err := io.Copy(zipWriter, input); err != nil {
		_ = zipWriter.Close()
		_ = tmp.Close()
		return err
	}
	if err := zipWriter.Close(); err != nil {
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
	compressedPath := path + ".gz"
	if err := os.Rename(tmpPath, compressedPath); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		_ = os.Remove(compressedPath)
		return err
	}
	return nil
}

func (w *rotatingFileLogger) pruneArchives(now time.Time) error {
	entries, err := os.ReadDir(filepath.Dir(w.config.Filename))
	if err != nil {
		return err
	}
	baseName := filepath.Base(w.config.Filename)
	type archiveInfo struct {
		path    string
		modTime time.Time
	}
	archives := make([]archiveInfo, 0, len(entries))
	cutoff := time.Time{}
	if w.config.MaxAgeDays > 0 {
		cutoff = now.Add(-time.Duration(w.config.MaxAgeDays) * 24 * time.Hour)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		match := rotatedLogName.FindStringSubmatch(entry.Name())
		if match == nil || match[1] != baseName {
			continue
		}
		path := filepath.Join(filepath.Dir(w.config.Filename), entry.Name())
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if !cutoff.IsZero() && info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		archives = append(archives, archiveInfo{path: path, modTime: info.ModTime()})
	}
	sort.Slice(archives, func(i, j int) bool { return archives[i].modTime.After(archives[j].modTime) })
	if w.config.MaxBackups > 0 && len(archives) > w.config.MaxBackups {
		for _, archive := range archives[w.config.MaxBackups:] {
			if err := os.Remove(archive.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func (w *rotatingFileLogger) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		if err := w.file.Sync(); err != nil {
			fmt.Fprintf(os.Stderr, "NPS log flush failed: %v\n", err)
		}
	}
}

func (w *rotatingFileLogger) Destroy() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	file := w.file
	w.file = nil
	w.mu.Unlock()
	if file != nil {
		_ = file.Sync()
		_ = file.Close()
	}
	if w.archives != nil {
		close(w.archives)
	}
	w.workerWG.Wait()
}
