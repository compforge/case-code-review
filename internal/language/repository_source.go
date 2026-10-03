package language

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type repositoryEntry struct {
	path string
	size int64
}

func (a *Analyzer) repositoryEntries(ctx context.Context) ([]repositoryEntry, string, error) {
	var entries []repositoryEntry
	if a.ref != "" {
		resolved, err := a.git.Output(ctx, a.repoDir, "rev-parse", "--verify", "--end-of-options", a.ref+"^{commit}")
		if err != nil {
			return nil, "", fmt.Errorf("resolve graph snapshot: %w", err)
		}
		snapshot := strings.TrimSpace(string(resolved))
		tree, err := a.git.Output(ctx, a.repoDir, "ls-tree", "-rlz", "--full-tree", snapshot)
		if err != nil {
			return nil, snapshot, fmt.Errorf("list graph snapshot: %w", err)
		}
		for _, record := range strings.Split(string(tree), "\x00") {
			metadata, path, ok := strings.Cut(record, "\t")
			if !ok {
				continue
			}
			fields := strings.Fields(metadata)
			if len(fields) != 4 || (fields[0] != "100644" && fields[0] != "100755") || skipRepositoryPath(path) {
				continue
			}
			size, err := strconv.ParseInt(fields[3], 10, 64)
			if err != nil {
				return nil, snapshot, fmt.Errorf("source size for %s: %w", path, err)
			}
			entries = append(entries, repositoryEntry{path: path, size: size})
		}
		return entries, snapshot, nil
	}
	err := filepath.WalkDir(a.repoDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(a.repoDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if rel != "." && skipRepositoryPath(rel+"/") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		entries = append(entries, repositoryEntry{path: rel, size: info.Size()})
		return nil
	})
	return entries, "review-worktree", err
}

func skipRepositoryPath(path string) bool {
	parts := strings.Split(path, "/")
	for _, dir := range parts[:len(parts)-1] {
		if strings.HasPrefix(dir, ".") || repositorySkipDirs[dir] {
			return true
		}
	}
	return false
}

func (a *Analyzer) readRepositoryFile(ctx context.Context, snapshot, path string) ([]byte, error) {
	if content, ok := a.documents[path]; ok {
		return []byte(content), nil
	}
	if a.ref != "" {
		return a.git.Output(ctx, a.repoDir, "show", snapshot+":"+path)
	}
	return os.ReadFile(filepath.Join(a.repoDir, filepath.FromSlash(path)))
}
