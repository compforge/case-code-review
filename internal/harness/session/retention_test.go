package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func retentionFixture(t *testing.T, root, name string, age time.Duration, ended bool) string {
	t.Helper()
	path := filepath.Join(root, "repo", name+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	content := "{\"type\":\"session_start\",\"schema_version\":1}\n"
	if ended {
		content += "{\"type\":\"session_end\"}\n"
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	ageFile(t, path, age)
	return path
}
func ageFile(t *testing.T, path string, age time.Duration) {
	t.Helper()
	stamp := time.Now().Add(-age)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}
func assertExists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	if want && err != nil {
		t.Fatalf("missing %s: %v", path, err)
	}
	if !want && !os.IsNotExist(err) {
		t.Fatalf("expected removal of %s: %v", path, err)
	}
}

func TestCleanupAgeSizeAndDryRun(t *testing.T) {
	root := t.TempDir()
	old := retentionFixture(t, root, "old", 8*24*time.Hour, true)
	middle := retentionFixture(t, root, "middle", 3*24*time.Hour, true)
	newest := retentionFixture(t, root, "new", 2*time.Hour, true)
	log := strings.TrimSuffix(old, ".jsonl") + ".log"
	if err := os.WriteFile(log, []byte("stderr"), 0600); err != nil {
		t.Fatal(err)
	}
	ageFile(t, log, 8*24*time.Hour)
	info, _ := os.Stat(newest)
	policy := RetentionPolicy{MaxAge: 7 * 24 * time.Hour, MaxBytes: info.Size()}
	preview, err := Cleanup(root, policy, CleanupOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Removed != 2 || preview.RemainingBytes != info.Size() || preview.FreedBytes != info.Size()*2+6 {
		t.Fatalf("preview: %+v", preview)
	}
	for _, path := range []string{old, middle, newest, log} {
		assertExists(t, path, true)
	}
	actual, err := Cleanup(root, policy, CleanupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if actual != preview {
		t.Fatalf("actual %+v != preview %+v", actual, preview)
	}
	for _, path := range []string{old, middle, log} {
		assertExists(t, path, false)
	}
	assertExists(t, newest, true)
	// Already within budget, equality does not evict the newest session.
	again, err := Cleanup(root, policy, CleanupOptions{})
	if err != nil || again.Removed != 0 {
		t.Fatalf("repeat %+v: %v", again, err)
	}
}

func TestCleanupAgeWithoutSizeAndDisabled(t *testing.T) {
	root := t.TempDir()
	path := retentionFixture(t, root, "old", 8*24*time.Hour, true)
	result, err := Cleanup(root, RetentionPolicy{}, CleanupOptions{})
	if err != nil || result.Removed != 0 {
		t.Fatalf("disabled %+v: %v", result, err)
	}
	result, err = Cleanup(root, RetentionPolicy{MaxAge: 7 * 24 * time.Hour}, CleanupOptions{})
	if err != nil || result.Removed != 1 {
		t.Fatalf("age %+v: %v", result, err)
	}
	assertExists(t, path, false)
}

func TestCleanupProtectsActiveRecentAndLegacyIncomplete(t *testing.T) {
	root := t.TempDir()
	active := retentionFixture(t, root, "active", 9*24*time.Hour, false)
	lease := flock.New(active + ".lock")
	if ok, err := lease.TryLock(); err != nil || !ok {
		t.Fatalf("lease %v %v", ok, err)
	}
	defer lease.Close()
	legacy := retentionFixture(t, root, "legacy", 8*24*time.Hour, false)
	recent := retentionFixture(t, root, "recent", time.Minute, true)
	orphan := retentionFixture(t, root, "orphan", 8*24*time.Hour, false)
	if err := os.WriteFile(orphan+".lock", nil, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := Cleanup(root, RetentionPolicy{MaxBytes: 1}, CleanupOptions{})
	if err != nil || result.Removed != 1 || result.Protected != 3 {
		t.Fatalf("result %+v: %v", result, err)
	}
	assertExists(t, orphan, false)
	assertExists(t, orphan+".lock", false)
	for _, path := range []string{active, legacy, recent} {
		assertExists(t, path, true)
	}
	result, err = Cleanup(root, RetentionPolicy{MaxBytes: 1}, CleanupOptions{IncludeIncomplete: true})
	if err != nil || result.Removed != 1 || result.Protected != 2 {
		t.Fatalf("explicit %+v: %v", result, err)
	}
	assertExists(t, legacy, false)
	assertExists(t, active, true)
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	result, err = Cleanup(root, RetentionPolicy{MaxBytes: 1}, CleanupOptions{})
	if err != nil || result.Removed != 1 {
		t.Fatalf("released %+v: %v", result, err)
	}
	assertExists(t, active, false)
}

func TestWriterLeaseLifecycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := filepath.Join(os.Getenv("HOME"), ".casecodereview", sessionSubDir)
	for _, finish := range []bool{false, true} {
		id := "interrupted"
		if finish {
			id = "finished"
		}
		writer, err := newJSONLWriter(id, "repo", "main", "test", SessionOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(writer.flushAndClose)
		writer.WriteSessionStart(time.Now())
		path := writer.file.Name()
		ageFile(t, path, 8*24*time.Hour)
		result, err := Cleanup(root, RetentionPolicy{MaxBytes: 1}, CleanupOptions{IncludeIncomplete: true})
		if err != nil || result.Removed != 0 || result.Protected != 1 {
			t.Fatalf("live %+v: %v", result, err)
		}
		if finish {
			writer.WriteSessionEnd(time.Second, nil, 0, diffStats{})
		} else {
			writer.flushAndClose()
		}
		ageFile(t, path, 8*24*time.Hour)
		result, err = Cleanup(root, RetentionPolicy{MaxBytes: 1}, CleanupOptions{})
		if err != nil || result.Removed != 1 {
			t.Fatalf("closed %+v: %v", result, err)
		}
	}
}

func TestCleanupSkipsSymlinksAndUnrelatedFiles(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := retentionFixture(t, outside, "external", 8*24*time.Hour, true)
	if err := os.Symlink(filepath.Dir(target), filepath.Join(root, "linked")); err != nil {
		t.Skip(err)
	}
	inside := retentionFixture(t, root, "inside", 8*24*time.Hour, true)
	link := filepath.Join(filepath.Dir(inside), "linked.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "config.json")
	if err := os.WriteFile(config, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := Cleanup(root, RetentionPolicy{MaxBytes: 1}, CleanupOptions{IncludeIncomplete: true})
	if err != nil || result.Removed != 1 {
		t.Fatalf("result %+v: %v", result, err)
	}
	assertExists(t, target, true)
	assertExists(t, link, true)
	assertExists(t, config, true)
}

func TestCleanupConcurrentAndMalformed(t *testing.T) {
	root := t.TempDir()
	path := retentionFixture(t, root, "broken", 8*24*time.Hour, false)
	if err := os.WriteFile(path, []byte("{broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ageFile(t, path, 8*24*time.Hour)
	guard := flock.New(filepath.Join(root, ".cleanup.lock"))
	if ok, err := guard.TryLock(); err != nil || !ok {
		t.Fatal(err)
	}
	result, err := Cleanup(root, RetentionPolicy{MaxBytes: 1}, CleanupOptions{})
	if err != nil || !result.Busy {
		t.Fatalf("busy %+v: %v", result, err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	result, err = Cleanup(root, RetentionPolicy{MaxBytes: 1}, CleanupOptions{})
	if err != nil || result.Protected != 1 {
		t.Fatalf("malformed %+v: %v", result, err)
	}
}

func TestCleanupKeepsChangedCandidate(t *testing.T) {
	root := t.TempDir()
	path := retentionFixture(t, root, "changed", 8*24*time.Hour, true)
	entries, err := collectSessions(root)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{\"type\":\"session_end\"}\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	freed, removed, protected, err := pruneSession(entries[0], CleanupOptions{})
	if err != nil || freed != 0 || removed || !protected {
		t.Fatalf("changed: %d %v %v %v", freed, removed, protected, err)
	}
	assertExists(t, path, true)
}

func TestSessionEndLargeAndTruncatedRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	for _, tc := range []struct {
		name, content string
		ended         bool
	}{
		{"large terminal", "{\"type\":\"session_start\"}\n{\"type\":\"session_end\",\"extra\":\"" + strings.Repeat("x", 64000) + "\"}\n", true},
		{"oversized terminal", "{\"type\":\"session_end\",\"extra\":\"" + strings.Repeat("x", 2*1024*1024) + "\"}\n", false},
		{"truncated", "{\"type\":\"session_start\"}\n{\"type\":\"session_end\"", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			ended, err := hasSessionEnd(path)
			if err != nil || ended != tc.ended {
				t.Fatalf("ended=%v err=%v", ended, err)
			}
		})
	}
}
