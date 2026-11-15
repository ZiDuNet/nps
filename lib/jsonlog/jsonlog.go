// Package jsonlog provides bounded tail reads and conservative, atomic cleanup
// for the append-only JSONL journals. It does not manage runtime text logs.
package jsonlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	RecentLimit    = 2000
	maxRecentBytes = 32 << 20
	readBlockSize  = 64 << 10
	maxRecordBytes = 1 << 20
)

// ReadRecent reads at most the last limit non-empty physical records. The byte
// budget also bounds work for corrupt files with missing line separators.
// Selection precedes filtering: a failed search never scans the entire file.
func ReadRecent(file *os.File, limit int) ([]byte, error) {
	if limit <= 0 || limit > RecentLimit {
		limit = RecentLimit
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("journal is not a regular file")
	}
	end, lines, total := info.Size(), 0, 0
	blocks := make([][]byte, 0)
	for end > 0 && lines <= limit && total < maxRecentBytes {
		length := min(int64(readBlockSize), end, int64(maxRecentBytes-total))
		block := make([]byte, length)
		if _, err := file.ReadAt(block, end-length); err != nil {
			return nil, fmt.Errorf("read journal tail: %w", err)
		}
		blocks = append(blocks, block)
		lines += bytes.Count(block, []byte{'\n'})
		total += len(block)
		end -= length
	}
	data := make([]byte, 0, total)
	for i := len(blocks) - 1; i >= 0; i-- {
		data = append(data, blocks[i]...)
	}
	if end > 0 {
		// The oldest block normally begins inside a line; never decode that
		// fragment as a complete event, even when it happens to be valid JSON.
		boundary := bytes.IndexByte(data, '\n')
		if boundary < 0 {
			return nil, nil
		}
		data = data[boundary+1:]
	}
	position, count := len(data), 0
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] != '\n' {
			continue
		}
		if len(bytes.TrimSpace(data[i+1:position])) > 0 {
			count++
			if count == limit {
				return skipOversizedRecords(data[i+1:]), nil
			}
		}
		position = i
	}
	return skipOversizedRecords(data), nil
}

// Keep a corrupt oversized record from exceeding the journals' scanner limit
// and hiding every later valid event. Compact in place within the owned tail.
func skipOversizedRecords(data []byte) []byte {
	kept := 0
	for rest := data; len(rest) > 0; {
		end := bytes.IndexByte(rest, '\n') + 1
		if end == 0 {
			end = len(rest)
		}
		if end < maxRecordBytes {
			kept += copy(data[kept:], rest[:end])
		}
		rest = rest[end:]
	}
	return data[:kept]
}

type CleanupResult struct {
	Removed      int64 `json:"removed"`
	RemovedBytes int64 `json:"removed_bytes"`
	Kept         int64 `json:"kept"`
}

var cleanupMu sync.Mutex

// PruneBefore removes only complete JSON records with a known timestamp that
// is strictly before cutoff. Unknown, malformed, and oversized lines survive.
// The caller's writer lock is held only for the initial snapshot and final
// append catch-up/replacement, not while scanning the potentially large file.
func PruneBefore(path string, cutoff time.Time, writer sync.Locker) (CleanupResult, error) {
	if cutoff.IsZero() {
		return CleanupResult{}, errors.New("cleanup cutoff is required")
	}
	if path == "" {
		return CleanupResult{}, nil
	}
	if !cleanupMu.TryLock() {
		return CleanupResult{}, errors.New("another journal cleanup is in progress")
	}
	defer cleanupMu.Unlock()

	writer.Lock()
	target, err := filepath.EvalSymlinks(path)
	var source *os.File
	var info os.FileInfo
	if err == nil {
		info, err = os.Stat(target)
	}
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("journal is not a regular file")
	}
	if err == nil {
		source, err = os.Open(target)
	}
	if err == nil {
		info, err = source.Stat()
	}
	writer.Unlock()
	if source != nil {
		defer source.Close()
	}
	if errors.Is(err, os.ErrNotExist) {
		return CleanupResult{}, nil
	}
	if err != nil {
		return CleanupResult{}, err
	}
	if !info.Mode().IsRegular() {
		return CleanupResult{}, errors.New("journal is not a regular file")
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".nps-journal-cleanup-*")
	if err != nil {
		return CleanupResult{}, err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		return CleanupResult{}, err
	}

	result, err := copyRetained(io.NewSectionReader(source, 0, info.Size()), temp, cutoff)
	if err != nil {
		return CleanupResult{}, err
	}
	if result.Removed == 0 {
		return result, nil
	}
	// Flush the expensive initial rewrite while normal appends can proceed.
	if err := temp.Sync(); err != nil {
		return CleanupResult{}, err
	}
	writer.Lock()
	defer writer.Unlock()
	currentTarget, err := filepath.EvalSymlinks(path)
	if err != nil || currentTarget != target {
		return CleanupResult{}, errors.New("journal changed during cleanup; retry")
	}
	current, err := os.Stat(target)
	if err != nil || !os.SameFile(info, current) || current.Size() < info.Size() {
		return CleanupResult{}, errors.New("journal was replaced or truncated during cleanup; retry")
	}
	// Preserve every append made during the scan, including a backdated event.
	// It was not part of the cleanup snapshot and must not be lost on rename.
	if _, err := io.Copy(temp, io.NewSectionReader(source, info.Size(), current.Size()-info.Size())); err != nil {
		return CleanupResult{}, err
	}
	if err := temp.Sync(); err != nil {
		return CleanupResult{}, err
	}
	if err := temp.Close(); err != nil {
		return CleanupResult{}, err
	}
	if err := source.Close(); err != nil {
		return CleanupResult{}, err
	}
	if err := os.Rename(temp.Name(), target); err != nil {
		return CleanupResult{}, err
	}
	return result, nil
}

func copyRetained(source io.Reader, dest io.Writer, cutoff time.Time) (CleanupResult, error) {
	reader := bufio.NewReaderSize(source, readBlockSize)
	var result CleanupResult
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			// Preserve arbitrarily long malformed/unknown records without
			// allocating an arbitrarily large buffer or aborting cleanup.
			result.Kept++
			for {
				if _, writeErr := dest.Write(line); writeErr != nil {
					return CleanupResult{}, writeErr
				}
				if !errors.Is(err, bufio.ErrBufferFull) {
					break
				}
				line, err = reader.ReadSlice('\n')
			}
		} else if len(line) > 0 {
			var stamp struct {
				Time    time.Time `json:"time"`
				EndedAt time.Time `json:"ended_at"`
			}
			valid := json.Unmarshal(bytes.TrimSpace(line), &stamp) == nil
			if stamp.Time.IsZero() {
				stamp.Time = stamp.EndedAt
			}
			if valid && !stamp.Time.IsZero() && stamp.Time.Before(cutoff) && line[len(line)-1] == '\n' {
				result.Removed++
				result.RemovedBytes += int64(len(line))
			} else {
				result.Kept++
				if _, writeErr := dest.Write(line); writeErr != nil {
					return CleanupResult{}, writeErr
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return result, nil
		}
		if err != nil {
			return CleanupResult{}, err
		}
	}
}
