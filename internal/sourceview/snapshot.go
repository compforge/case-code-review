// Package sourceview owns the immutable source inputs shared by review consumers.
package sourceview

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/qiankunli/case-code-review/internal/gitcmd"
)

// File is a captured overlay entry. Missing entries remain unavailable, rather
// than falling through to an older version of the same path.
type File struct {
	Content     string
	Deleted     bool
	Unavailable bool
}

type Entry struct {
	Path string
	Size int64
}

// Snapshot is a fixed Git tree with captured workspace changes overlaid on it.
// An empty Ref means the empty tree, never the live workspace.
//
// +spec=`All review consumers read identical bytes for a path in one snapshot; missing captured content never falls back to live files`
// +why=`A fixed baseline plus captured changes gives workspace review stable inputs without copying the whole repository`
type Snapshot struct {
	RepoDir    string
	Ref        string
	ID         string
	git        *gitcmd.Runner
	overlay    map[string]File
	once       sync.Once
	entries    []Entry
	entriesErr error
}

func New(repoDir, ref, id string, overlay map[string]File, runner *gitcmd.Runner) *Snapshot {
	if runner == nil {
		runner = gitcmd.New(0)
	}
	return &Snapshot{RepoDir: repoDir, Ref: ref, ID: id, overlay: maps.Clone(overlay), git: runner}
}

func (s *Snapshot) Read(ctx context.Context, name string) (string, error) {
	name = path.Clean(name)
	if !fs.ValidPath(name) {
		return "", fmt.Errorf("invalid snapshot path %q", name)
	}
	if f, ok := s.overlay[name]; ok {
		if f.Deleted {
			return "", fmt.Errorf("%s: %w", name, fs.ErrNotExist)
		}
		if f.Unavailable {
			return "", fmt.Errorf("%s: source unavailable when diff was captured", name)
		}
		return f.Content, nil
	}
	if s.Ref == "" {
		return "", fmt.Errorf("%s: %w", name, fs.ErrNotExist)
	}
	// Listing distinguishes absence from Git failures without interpreting stderr.
	entries, err := s.Entries(ctx)
	if err != nil {
		return "", err
	}
	i := sort.Search(len(entries), func(i int) bool { return entries[i].Path >= name })
	if i == len(entries) || entries[i].Path != name {
		return "", fmt.Errorf("%s: %w", name, fs.ErrNotExist)
	}
	data, err := s.git.Output(ctx, s.RepoDir, "show", "--end-of-options", s.Ref+":"+name)
	return string(data), err
}

func (s *Snapshot) Entries(ctx context.Context) ([]Entry, error) {
	s.once.Do(func() {
		files := map[string]int64{}
		if s.Ref != "" {
			data, err := s.git.Output(ctx, s.RepoDir, "ls-tree", "-rlz", "--full-tree", s.Ref)
			if err != nil {
				s.entriesErr = err
				return
			}
			for _, record := range strings.Split(string(data), "\x00") {
				metadata, name, ok := strings.Cut(record, "\t")
				fields := strings.Fields(metadata)
				if !ok || len(fields) != 4 || (fields[0] != "100644" && fields[0] != "100755") {
					continue
				}
				size, err := strconv.ParseInt(fields[3], 10, 64)
				if err != nil {
					s.entriesErr = err
					return
				}
				files[name] = size
			}
		}
		for name, f := range s.overlay {
			if f.Deleted {
				delete(files, name)
			} else {
				files[name] = int64(len(f.Content))
			}
		}
		for name, size := range files {
			s.entries = append(s.entries, Entry{name, size})
		}
		sort.Slice(s.entries, func(i, j int) bool { return s.entries[i].Path < s.entries[j].Path })
	})
	return s.entries, s.entriesErr
}
