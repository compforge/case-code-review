// Package viewer provides a read-only WebUI for browsing session records
// produced by case-code-review runs. It scans JSONL files under
// $HOME/.casecodereview/sessions/, parses them, and exposes structured data.
package viewer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/qiankunli/case-code-review/internal/recording"
	"github.com/qiankunli/case-code-review/internal/recording/operation"
)

// SessionsRoot returns the root directory where session JSONL files are stored.
func SessionsRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".casecodereview", "sessions"), nil
}

// RepoInfo represents a discovered repository from the sessions directory.
type RepoInfo struct {
	EncodedPath  string // encoded directory name on disk
	DisplayName  string
	SessionCount int
	LastModified time.Time
	LatestBizID  string
}

// DiscoverRepos walks the sessions root and returns one entry per subdirectory.
func DiscoverRepos(root string) ([]RepoInfo, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sessions dir: %w", err)
	}

	var repos []RepoInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		repoDir := filepath.Join(root, e.Name())
		info := RepoInfo{EncodedPath: e.Name()}
		latestSessionPath := ""

		subEntries, err := os.ReadDir(repoDir)
		if err != nil {
			continue
		}
		for _, se := range subEntries {
			if !strings.HasSuffix(se.Name(), ".jsonl") {
				continue
			}
			path := filepath.Join(repoDir, se.Name())
			if _, err := readSessionStart(path); err != nil {
				continue
			}
			info.SessionCount++
			if fi, err := se.Info(); err == nil && fi.ModTime().After(info.LastModified) {
				info.LastModified = fi.ModTime()
				latestSessionPath = path
			}
		}
		if info.SessionCount > 0 {
			summary, _ := readSessionStart(latestSessionPath)
			info.LatestBizID = summary.BizID
			info.DisplayName = filepath.Base(summary.CWD)
			if info.DisplayName == "." || info.DisplayName == "" {
				info.DisplayName = info.EncodedPath
			}
			repos = append(repos, info)
		}
	}

	sort.Slice(repos, func(i, j int) bool {
		return repos[i].LastModified.After(repos[j].LastModified)
	})
	return repos, nil
}

func readSessionStart(path string) (SessionSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionSummary{}, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return SessionSummary{}, fmt.Errorf("session is empty")
	}
	var record map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
		return SessionSummary{}, err
	}
	if stringValue(record["type"]) != "session_start" {
		return SessionSummary{}, fmt.Errorf("first record is not session_start")
	}
	version := intValue(record["schema_version"])
	if version != recording.SchemaVersion {
		return SessionSummary{}, fmt.Errorf("unsupported session schema %d", version)
	}
	summary := SessionSummary{
		SchemaVersion: version,
		CWD:           stringValue(record["cwd"]), GitBranch: stringValue(record["gitBranch"]),
		Model: stringValue(record["model"]), ReviewMode: stringValue(record["reviewMode"]),
		DiffFrom: stringValue(record["diffFrom"]), DiffTo: stringValue(record["diffTo"]),
		DiffCommit: stringValue(record["diffCommit"]), BizID: stringValue(record["biz_id"]),
	}
	if raw := stringValue(record["timestamp"]); raw != "" {
		summary.Timestamp, _ = time.Parse(time.RFC3339, raw)
	}
	return summary, nil
}

// SessionSummary is built from session_start and session_end records.
type SessionSummary struct {
	Completion     recording.Completion
	Tokens         *recording.TokenUsage
	SchemaVersion  int
	SessionID      string
	Timestamp      time.Time
	CWD            string
	GitBranch      string
	Model          string
	ReviewMode     string
	DiffFrom       string
	DiffTo         string
	DiffCommit     string
	BizID          string
	FilesReviewed  []string
	DiffFileCount  int
	DiffInsertions int
	DiffDeletions  int
	HasDiffStats   bool
	DurationSec    float64
	FileCount      int
	LLMFailures    int
}

// ListSessions returns lightweight summaries for all sessions in a repo subdir.
func ListSessions(root, encodedRepo string) ([]SessionSummary, error) {
	repoDir := filepath.Join(root, encodedRepo)
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		return nil, fmt.Errorf("read repo dir: %w", err)
	}

	var summaries []SessionSummary
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		sessionID := strings.TrimSuffix(e.Name(), ".jsonl")
		s, err := peekSession(filepath.Join(repoDir, e.Name()))
		if err != nil {
			continue // skip unreadable files
		}
		s.SessionID = sessionID
		summaries = append(summaries, s)
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Timestamp.After(summaries[j].Timestamp)
	})
	return summaries, nil
}

// peekSession reads only the first and last record of a JSONL file.
func peekSession(path string) (SessionSummary, error) {
	summary, err := readSessionStart(path)
	if err != nil {
		return SessionSummary{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return SessionSummary{}, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	var lastLine []byte
	for scanner.Scan() {
		line := scanner.Bytes()
		lastLine = append([]byte(nil), line...)
	}

	if len(lastLine) > 0 {
		var rec map[string]any
		if err := json.Unmarshal(lastLine, &rec); err == nil {
			if typ, _ := rec["type"].(string); typ == "session_end" {
				transcript := recording.Transcript{Records: []map[string]any{rec}}
				summary.Completion = transcript.Completion()
				if raw, err := json.Marshal(rec["tokens"]); err == nil {
					_ = json.Unmarshal(raw, &summary.Tokens)
				}
				if dur, ok := rec["duration_seconds"].(float64); ok {
					summary.DurationSec = dur
				}
				if files, ok := rec["files_reviewed"].([]any); ok {
					summary.FilesReviewed = make([]string, 0, len(files))
					for _, fv := range files {
						if s, ok := fv.(string); ok {
							summary.FilesReviewed = append(summary.FilesReviewed, s)
						}
					}
				}
				if n, ok := rec["diff_files"].(float64); ok {
					summary.DiffFileCount = int(n)
					summary.HasDiffStats = true
				}
				if n, ok := rec["diff_insertions"].(float64); ok {
					summary.DiffInsertions = int(n)
				}
				if n, ok := rec["diff_deletions"].(float64); ok {
					summary.DiffDeletions = int(n)
				}
				if f, ok := rec["llm_failures"].(float64); ok {
					summary.LLMFailures = int(f)
				}
			}
		}
	}
	if summary.Completion.Status == "" {
		summary.Completion.Status = "running"
	}
	summary.FileCount = len(summary.FilesReviewed)
	return summary, scanner.Err()
}

// ViewSession is the lightweight business Timeline page projection.
// Model and tool bodies belong to the selected Stage view.
type ViewSession struct {
	Jobs              []recording.UnitResult
	RecordingWarnings []string
	Journal           *operation.Snapshot
	Summary           SessionSummary
	CompletionLabel   string
	Findings          int
}

// LoadSession projects business records and the operation. Call contents are
// loaded by the selected Stage view, never by page navigation.
func LoadSession(root, encodedRepo, sessionID string) (*ViewSession, error) {
	path := filepath.Join(root, encodedRepo, sessionID+".jsonl")
	summary, err := peekSession(path)
	if err != nil {
		return nil, err
	}
	transcript, err := recording.ReadJournal(path)
	if err != nil {
		return nil, err
	}
	summary.SessionID = sessionID
	summary.Completion = transcript.Completion()
	vs := &ViewSession{Summary: summary, RecordingWarnings: transcript.Gaps, Jobs: transcript.Jobs()}
	if transcript.Journal.ID != "" {
		vs.Journal = &transcript.Journal
	}
	if transcript.TruncatedTail {
		vs.RecordingWarnings = append(vs.RecordingWarnings, "Recording has an incomplete final line.")
	}
	vs.CompletionLabel = completionLabel(summary.Completion)
	for _, record := range transcript.Records {
		if record["type"] == "finding" {
			vs.Findings++
		}
		if record["type"] == "artifact" && record["artifact_kind"] == "recording_gap" {
			if data, ok := record["data"].(map[string]any); ok {
				vs.RecordingWarnings = append(vs.RecordingWarnings, stringValue(data["error"]))
			}
		}
	}

	return vs, nil
}

func completionLabel(result recording.Completion) string {
	switch result.Status {
	case "completed":
		return "Complete"
	case "partial":
		return "Partial — not a clean verdict"
	case "failed":
		return "Failed — not a clean verdict"
	case "running":
		return "Not finalized"
	default:
		return "Completion unknown"
	}
}

// stringList coerces a JSON value (expected []any of strings) into []string.
func stringList(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func intValue(value any) int {
	switch number := value.(type) {
	case float64:
		return int(number)
	case int:
		return number
	case int64:
		return int(number)
	default:
		return 0
	}
}
