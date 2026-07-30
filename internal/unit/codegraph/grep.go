package codegraph

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/qiankunli/case-code-review/internal/gitcmd"
	"github.com/qiankunli/case-code-review/internal/language"
)

const (
	defaultMaxResults = 8
	grepTimeout       = 10 * time.Second
)

type hit struct {
	file string
	line int
	text string // the matched line's content (usage-site rendering; ignored by callers that only resolve)
}

// grepCode runs `git grep` over the repo's function-aware source files with the given match
// args (e.g. ["-w", "-e", name] for a word match, or ["-P", "-e", pattern] for a
// regex) and returns up to maxHits file:line hits. When scopeDir is non-empty the
// grep is restricted to that single package directory (see scopePathspecs).
// Returns nil on any error so finders degrade silently.
func grepCode(repoDir string, runner *gitcmd.Runner, matchArgs []string, maxHits int, scopeDir string) []hit {
	ctx, cancel := context.WithTimeout(context.Background(), grepTimeout)
	defer cancel()

	args := append([]string{"--no-pager", "grep", "-n", "--no-color"}, matchArgs...)
	// funcIDAt dispatches per hit file by extension.
	args = append(args, "--")
	args = append(args, scopePathspecs(scopeDir)...)
	out, err := gitOutput(ctx, repoDir, runner, args)
	if err != nil {
		return nil
	}
	var hits []hit
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3) // file:line:content
		if len(parts) < 3 {
			continue
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		hits = append(hits, hit{file: parts[0], line: n, text: parts[2]})
		if maxHits > 0 && len(hits) >= maxHits {
			break
		}
	}
	return hits
}

// scopePathspecs restricts the grep to one package directory when scopeDir is
// set — an unexported Go symbol's callers/defs can only live in its own package,
// so this kills cross-package same-name false positives. :(glob) keeps * from
// crossing '/', so only the directory's direct files match (Go packages are
// non-recursive). Empty scopeDir greps the whole repo (the default).
func scopePathspecs(scopeDir string) []string {
	var patterns []string
	for _, extension := range language.StructuredExtensions() {
		glob := "*" + extension
		switch scopeDir {
		case "":
			patterns = append(patterns, glob)
		case ".":
			patterns = append(patterns, ":(glob)"+glob)
		default:
			patterns = append(patterns, ":(glob)"+scopeDir+"/"+glob)
		}
	}
	return patterns
}

func gitOutput(ctx context.Context, repoDir string, runner *gitcmd.Runner, args []string) ([]byte, error) {
	if runner != nil {
		return runner.Output(ctx, repoDir, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoDir
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// funcIDAt reads the hit's file (under repoDir) and resolves the line to the
// symbol-id of its enclosing function — dispatched by extension.
func funcIDAt(analyzer *language.Analyzer, repoDir string, h hit) (string, bool) {
	src, err := os.ReadFile(filepath.Join(repoDir, h.file))
	if err != nil {
		return "", false
	}
	if analyzer == nil {
		analyzer = language.NewAnalyzer(repoDir)
	}
	definition, ok := analyzer.DefinitionAt(context.Background(), language.Source{
		Path: h.file, Content: string(src),
	}, h.line)
	return definition.SymbolID, ok
}
