package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qiankunli/case-code-review/internal/harness/session"
)

func TestRetentionConfiguration(t *testing.T) {
	cfg := &Config{}
	days, mib := retentionLimits(cfg)
	if days != 7 || mib != 5120 {
		t.Fatalf("defaults: %d %d", days, mib)
	}
	if err := setConfigValue(cfg, "retention.days", "0"); err != nil {
		t.Fatal(err)
	}
	if err := setConfigValue(cfg, "retention.max_mib", "25"); err != nil {
		t.Fatal(err)
	}
	days, mib = retentionLimits(cfg)
	policy, err := retentionPolicy(days, mib)
	if err != nil || policy.MaxAge != 0 || policy.MaxBytes != 25*(1<<20) {
		t.Fatalf("policy %+v: %v", policy, err)
	}
	for _, value := range []string{"-1", "not-a-number", "9223372036854775807"} {
		for _, key := range []string{"retention.days", "retention.max_mib"} {
			if err := setConfigValue(cfg, key, value); err == nil {
				t.Fatalf("accepted %s=%s", key, value)
			}
		}
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadAppConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	days, mib = retentionLimits(loaded)
	if days != 0 || mib != 25 {
		t.Fatalf("roundtrip %d %d", days, mib)
	}
}

func cleanupCLIFixture(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, "repo", name+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\"type\":\"session_end\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCleanCommandDryRunAndStoreSelection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root, _ := sessionCleanupRoot(false)
	tests, _ := sessionCleanupRoot(true)
	path := cleanupCLIFixture(t, root, "normal")
	testPath := cleanupCLIFixture(t, tests, "test")
	if err := runClean([]string{"--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if err := runClean([]string{"--test-sessions"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(testPath); !os.IsNotExist(err) {
		t.Fatalf("test store not cleaned: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if err := runClean(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("normal store not cleaned: %v", err)
	}
	for _, args := range [][]string{{"--days", "-1"}, {"--max-mib", "-1"}, {"unexpected"}} {
		if err := runClean(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestRecordingCleanupRepeatsAndStops(t *testing.T) {
	root := t.TempDir()
	old := cleanupCLIFixture(t, root, "initial")
	stop := startCleanupLoop(root, session.RetentionPolicy{MaxAge: 7 * 24 * time.Hour}, 5*time.Millisecond)
	// Always stop the worker before TempDir is removed, including on failure.
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("initial cleanup: %v", err)
	}
	later := cleanupCLIFixture(t, root, "later")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(later); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("periodic cleanup did not run")
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	stop = nil
	preserved := cleanupCLIFixture(t, root, "after-stop")
	time.Sleep(20 * time.Millisecond)
	if _, err := os.Stat(preserved); err != nil {
		t.Fatalf("cleanup continued after stop: %v", err)
	}
}

func TestDisabledAutomaticCleanup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root, _ := sessionCleanupRoot(false)
	path := cleanupCLIFixture(t, root, "old")
	zero := int64(0)
	stop := startSessionCleanup(&Config{Retention: &RetentionConfig{Days: &zero, MaxMiB: &zero}})
	stop()
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
