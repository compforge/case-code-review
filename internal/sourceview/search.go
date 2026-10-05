package sourceview

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Grep uses Git's own regexp and pathspec semantics on the fixed tree and a
// temporary overlay. The temporary files contain only already captured bytes;
// no search can observe later working-tree edits.
func (s *Snapshot) Grep(ctx context.Context, args []string, outputRef string) (string, string, error) {
	var flags, patterns []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			patterns = args[i+1:]
			break
		}
		if arg == "--untracked" || arg == "--exclude-standard" {
			continue
		}
		if arg == "--end-of-options" {
			i++
			continue
		}
		flags = append(flags, arg)
	}
	listing := false
	for _, arg := range flags {
		if arg == "-l" {
			listing = true
		}
	}
	separator := "\n"
	if listing {
		separator = "\x00"
	}
	var matches []string
	var gaps []string
	collect := func(out string, baseline bool) {
		for _, line := range strings.Split(out, separator) {
			if line == "" {
				continue
			}
			line = strings.TrimPrefix(line, s.Ref+":")
			line = strings.TrimPrefix(line, "./")
			name := line
			if !listing {
				name, _, _ = strings.Cut(line, ":")
			}
			if baseline {
				if _, changed := s.overlay[name]; changed {
					continue
				}
			}
			if outputRef != "" {
				line = outputRef + ":" + line
			}
			matches = append(matches, line)
		}
	}
	run := func(dir string, opts []string, baseline bool) (string, error) {
		out, stderr, err := s.git.RunSplit(ctx, dir, opts...)
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || stderr != "" {
				return stderr, err
			}
		}
		collect(out, baseline)
		return "", nil
	}
	if s.Ref != "" {
		opts := append(append([]string{}, flags...), "--end-of-options", s.Ref, "--")
		if stderr, err := run(s.RepoDir, append(opts, patterns...), true); err != nil {
			return "", stderr, err
		}
	}
	if len(s.overlay) > 0 {
		dir, err := os.MkdirTemp("", "ccr-source-search-")
		if err != nil {
			return "", "", err
		}
		defer os.RemoveAll(dir)
		hasFiles := false
		for name, f := range s.overlay {
			if err := ctx.Err(); err != nil {
				return "", "", err
			}
			if f.Deleted {
				continue
			}
			if f.Unavailable {
				gaps = append(gaps, name+": source unavailable at capture")
				continue
			}
			// The capture boundary admits repository-relative paths only.
			if !filepath.IsLocal(name) {
				return "", "", fmt.Errorf("invalid captured path %q", name)
			}
			target := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return "", "", err
			}
			if err := os.WriteFile(target, []byte(f.Content), 0600); err != nil {
				return "", "", err
			}
			hasFiles = true
		}
		if hasFiles {
			opts := append(append([]string{}, flags...), "--no-index", "--")
			if stderr, err := run(dir, append(opts, patterns...), false); err != nil {
				return "", stderr, err
			}
		}
	}
	sort.Strings(matches)
	if len(gaps) > 0 {
		sort.Strings(gaps)
		detail := strings.Join(gaps, "; ")
		return strings.Join(matches, separator), detail, fmt.Errorf("incomplete source search: %s", detail)
	}
	if len(matches) == 0 {
		return "", "", nil
	}
	return strings.Join(matches, separator) + separator, "", nil
}
