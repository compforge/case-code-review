package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// RetentionPolicy rotates whole sessions, preserving the records needed by all
// transcript readers. Zero disables that limit. Active and recent sessions can
// temporarily put the store over its size budget.
type RetentionPolicy struct {
	MaxAge   time.Duration
	MaxBytes int64
}

// CleanupOptions controls an explicit maintenance invocation.
type CleanupOptions struct {
	DryRun bool
	// IncludeIncomplete allows deleting legacy transcripts without a lease or
	// session_end. Callers must stop older CCR processes before using this option.
	IncludeIncomplete bool
}

type CleanupResult struct {
	Sessions       int
	Removed        int
	FreedBytes     int64
	RemainingBytes int64
	Protected      int
	Busy           bool
}

type retainedSession struct {
	path     string
	files    []retainedFile
	modified time.Time
	size     int64
}
type retainedFile struct {
	path string
	size int64
}

// Cleanup prunes oldest sessions in root/<repo>/*.jsonl and their sibling .log
// files. It never truncates a transcript. Only idle leases or a terminal record
// prove inactivity; legacy incomplete files require an explicit opt-in.
func Cleanup(root string, policy RetentionPolicy, opts CleanupOptions) (CleanupResult, error) {
	var result CleanupResult
	if policy.MaxAge < 0 || policy.MaxBytes < 0 {
		return result, fmt.Errorf("retention limits must not be negative")
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !info.IsDir() {
		return result, fmt.Errorf("session root must be a directory: %s", root)
	}
	// Serialize maintenance processes, including dry runs. Writers use per-session
	// leases instead, so a long directory scan never stalls model output.
	guard := flock.New(filepath.Join(root, ".cleanup.lock"), flock.SetPermissions(0600))
	locked, err := guard.TryLock()
	if err != nil {
		return result, err
	}
	if !locked {
		result.Busy = true
		return result, nil
	}
	defer guard.Close()
	entries, err := collectSessions(root)
	if err != nil {
		return result, err
	}
	result.Sessions = len(entries)
	for _, entry := range entries {
		result.RemainingBytes += entry.size
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].modified.Equal(entries[j].modified) {
			return entries[i].path < entries[j].path
		}
		return entries[i].modified.Before(entries[j].modified)
	})
	now := time.Now()
	var failures []error
	for _, entry := range entries {
		expired := policy.MaxAge > 0 && now.Sub(entry.modified) > policy.MaxAge
		overBudget := policy.MaxBytes > 0 && result.RemainingBytes > policy.MaxBytes
		if !expired && !overBudget {
			continue
		}
		// Keep newly completed output available for immediate viewer/export use.
		if now.Sub(entry.modified) < time.Hour {
			result.Protected++
			continue
		}
		freed, removed, protected, err := pruneSession(entry, opts)
		result.FreedBytes += freed
		result.RemainingBytes -= freed
		if removed {
			result.Removed++
		}
		if protected {
			result.Protected++
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("clean %s: %w", entry.path, err))
		}
	}
	return result, errors.Join(failures...)
}

func collectSessions(root string) ([]retainedSession, error) {
	repos, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var entries []retainedSession
	for _, repo := range repos {
		if !repo.IsDir() {
			continue
		} // Never follow repository-directory symlinks.
		dir := filepath.Join(root, repo.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			if !strings.HasSuffix(file.Name(), ".jsonl") {
				continue
			}
			info, err := file.Info()
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() {
				continue
			}
			path := filepath.Join(dir, file.Name())
			entry := retainedSession{path: path, modified: info.ModTime(), size: info.Size()}
			logPath := strings.TrimSuffix(path, ".jsonl") + ".log"
			logInfo, err := os.Lstat(logPath)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			if err == nil && logInfo.Mode().IsRegular() {
				entry.files = append(entry.files, retainedFile{logPath, logInfo.Size()})
				entry.size += logInfo.Size()
				if logInfo.ModTime().After(entry.modified) {
					entry.modified = logInfo.ModTime()
				}
			}
			entry.files = append(entry.files, retainedFile{path, info.Size()})
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func pruneSession(entry retainedSession, opts CleanupOptions) (freed int64, removed, protected bool, err error) {
	lockPath := entry.path + ".lock"
	info, err := os.Lstat(lockPath)
	managed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, false, false, err
	}
	var lease *flock.Flock
	if managed {
		if !info.Mode().IsRegular() {
			return 0, false, true, nil
		}
		lease = flock.New(lockPath, flock.SetFlag(os.O_RDWR))
		locked, err := lease.TryLock()
		if err != nil {
			return 0, false, false, err
		}
		if !locked {
			return 0, false, true, nil
		}
		defer lease.Close()
	} else if !opts.IncludeIncomplete {
		ended, err := hasSessionEnd(entry.path)
		if err != nil {
			return 0, false, false, err
		}
		if !ended {
			return 0, false, true, nil
		}
	}
	// A legacy writer or log mirror can have appended while we scanned. Never
	// remove a changed candidate, even when incomplete cleanup was requested.
	for _, file := range entry.files {
		info, err := os.Lstat(file.path)
		if err != nil {
			return 0, false, false, err
		}
		if !info.Mode().IsRegular() || info.Size() != file.size || info.ModTime().After(entry.modified) {
			return 0, false, true, nil
		}
	}
	if opts.DryRun {
		return entry.size, true, false, nil
	}
	for _, file := range entry.files {
		if err := os.Remove(file.path); err != nil {
			return freed, false, false, err
		}
		freed += file.size
	}
	if managed {
		// IDs are unique; maintenance is serialized. Release before unlinking for Windows.
		if err := lease.Close(); err != nil {
			return freed, true, false, err
		}
		if err := os.Remove(lockPath); err != nil {
			return freed, true, false, err
		}
	}
	return freed, true, false, nil
}

// Read only the tail, independently of schema version: old recordings are the
// main retention target. An oversized or malformed final record stays protected.
func hasSessionEnd(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	const tailLimit = 1024 * 1024
	for size := int64(8192); ; size *= 2 {
		offset := max(int64(0), info.Size()-size)
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return false, err
		}
		tail, err := io.ReadAll(io.LimitReader(f, size))
		if err != nil {
			return false, err
		}
		tail = bytes.TrimSpace(tail)
		index := bytes.LastIndexByte(tail, '\n')
		if index < 0 && offset > 0 {
			if size >= tailLimit {
				return false, nil
			}
			continue
		}
		var record struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(tail[index+1:], &record) != nil {
			return false, nil
		}
		return record.Type == "session_end", nil
	}
}
