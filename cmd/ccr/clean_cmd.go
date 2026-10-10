package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/qiankunli/case-code-review/internal/harness/session"
)

// Pointers distinguish omitted defaults from an explicitly disabled limit.
type RetentionConfig struct {
	Days   *int64 `json:"days,omitempty"`
	MaxMiB *int64 `json:"max_mib,omitempty"`
}

func retentionLimits(cfg *Config) (days, mib int64) {
	days, mib = 7, 5120
	if cfg != nil && cfg.Retention != nil {
		if cfg.Retention.Days != nil {
			days = *cfg.Retention.Days
		}
		if cfg.Retention.MaxMiB != nil {
			mib = *cfg.Retention.MaxMiB
		}
	}
	return
}

func retentionPolicy(days, mib int64) (session.RetentionPolicy, error) {
	if days < 0 || days > int64((1<<63-1)/(24*time.Hour)) || mib < 0 || mib > (1<<63-1)/(1<<20) {
		return session.RetentionPolicy{}, fmt.Errorf("retention limits must be non-negative and fit their storage units")
	}
	return session.RetentionPolicy{MaxAge: time.Duration(days) * 24 * time.Hour, MaxBytes: mib * (1 << 20)}, nil
}

func setRetentionValue(cfg *Config, key, value string) error {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid %s: %w", key, err)
	}
	days, mib := retentionLimits(cfg)
	if key == "retention.days" {
		days = n
	} else {
		mib = n
	}
	if _, err := retentionPolicy(days, mib); err != nil {
		return err
	}
	if cfg.Retention == nil {
		cfg.Retention = &RetentionConfig{}
	}
	if key == "retention.days" {
		cfg.Retention.Days = &n
	} else {
		cfg.Retention.MaxMiB = &n
	}
	return nil
}

func sessionCleanupRoot(testSessions bool) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	name := "sessions"
	if testSessions {
		name = "test-sessions"
	}
	return filepath.Join(home, ".casecodereview", name), nil
}

func runClean(args []string) error {
	configPath, err := defaultConfigPath()
	if err != nil {
		return err
	}
	cfg, err := LoadAppConfig(configPath)
	if err != nil {
		return err
	}
	days, mib := retentionLimits(cfg)
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	fs.Int64Var(&days, "days", days, "Maximum session age in days (0 disables age limit)")
	fs.Int64Var(&mib, "max-mib", mib, "Maximum total session size in MiB (0 disables size limit)")
	dryRun := fs.Bool("dry-run", false, "Preview deletions without removing recordings")
	incomplete := fs.Bool("include-incomplete", false, "Include legacy unfinished sessions; stop older CCR processes first")
	testSessions := fs.Bool("test-sessions", false, "Clean the test-sessions store instead of sessions")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: ccr clean [options]\nPrune whole sessions, oldest first. Active and recently written (<1h) sessions are protected.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("clean does not accept positional arguments")
	}
	policy, err := retentionPolicy(days, mib)
	if err != nil {
		return err
	}
	root, err := sessionCleanupRoot(*testSessions)
	if err != nil {
		return err
	}
	result, err := session.Cleanup(root, policy, session.CleanupOptions{DryRun: *dryRun, IncludeIncomplete: *incomplete})
	printCleanup(result, *dryRun)
	return err
}

func printCleanup(result session.CleanupResult, dryRun bool) {
	if result.Busy {
		fmt.Println("Cleanup skipped: another cleanup is running.")
		return
	}
	verb := "Removed"
	if dryRun {
		verb = "Would remove"
	}
	fmt.Printf("%s %d/%d sessions, %.2f GiB; remaining %.2f GiB; protected candidates: %d\n",
		verb, result.Removed, result.Sessions, float64(result.FreedBytes)/(1<<30), float64(result.RemainingBytes)/(1<<30), result.Protected)
}

// Only recording runs own this lifecycle: previews and readers never prune.
// Periodic maintenance bounds growth during long runs without scanning the
// store on every JSONL append. The current writer's lease protects its output.
func startSessionCleanup(cfg *Config) func() {
	days, mib := retentionLimits(cfg)
	policy, err := retentionPolicy(days, mib)
	if err == nil && policy.MaxAge == 0 && policy.MaxBytes == 0 {
		return func() {}
	}
	root := ""
	if err == nil {
		root, err = sessionCleanupRoot(false)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ccr cleanup] %v\n", err)
		return func() {}
	}
	return startCleanupLoop(root, policy, 10*time.Minute)
}

func startCleanupLoop(root string, policy session.RetentionPolicy, interval time.Duration) func() {
	cleanSessions(root, policy)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				cleanSessions(root, policy)
			}
		}
	}()
	return func() { close(stop); <-done }
}

func cleanSessions(root string, policy session.RetentionPolicy) {
	result, err := session.Cleanup(root, policy, session.CleanupOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ccr cleanup] %v\n", err)
	}
	if result.Removed > 0 {
		fmt.Fprintf(os.Stderr, "[ccr cleanup] removed %d old sessions (%.2f GiB), remaining %.2f GiB\n", result.Removed, float64(result.FreedBytes)/(1<<30), float64(result.RemainingBytes)/(1<<30))
	}
	if policy.MaxBytes > 0 && result.RemainingBytes > policy.MaxBytes {
		fmt.Fprintln(os.Stderr, "[ccr cleanup] size limit remains exceeded; active/recent sessions and legacy incomplete records are protected. Inspect with ccr clean --dry-run.")
	}
}
