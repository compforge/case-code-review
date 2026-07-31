package pathutil

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

var excludedDirs = []string{
	".idea/",
	".vscode/",
	".svn/",
	".git/",
	"vendor/",
	"node_modules/",
	"target/",
	".happypack/",
	".cachefile/",
	"_packages/",
	"rpm/",
	"pkgs/",
}

// LoadGitignorePatterns reads the repository-root .gitignore. Git-backed
// callers should still prefer git ls-files for full nested/negation semantics.
func LoadGitignorePatterns(repoDir string) []string {
	data, err := os.ReadFile(filepath.Join(repoDir, ".gitignore"))
	if err != nil {
		return nil
	}
	var patterns []string
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns
}

// IsPathExcluded applies CCR's default artifact-directory exclusions plus the
// simplified root .gitignore rules used by non-git filesystem walks.
func IsPathExcluded(relPath string, patterns []string) bool {
	for _, prefix := range excludedDirs {
		dir := strings.TrimSuffix(prefix, "/")
		if relPath == dir || strings.HasPrefix(relPath, prefix) {
			return true
		}
	}
	for _, pattern := range patterns {
		if MatchGitignorePattern(relPath, pattern) {
			return true
		}
	}
	return false
}

// MatchGitignorePattern implements the deliberately small pattern subset used
// when git itself is unavailable.
func MatchGitignorePattern(relPath, pattern string) bool {
	if dir, ok := strings.CutSuffix(pattern, "/"); ok {
		return slices.Contains(strings.Split(relPath, "/"), dir)
	}
	if strings.HasPrefix(pattern, "!") {
		return false
	}
	if !strings.Contains(pattern, "/") {
		matched, _ := filepath.Match(pattern, filepath.Base(relPath))
		return matched
	}
	if matched, _ := filepath.Match(pattern, relPath); matched {
		return true
	}
	return strings.HasSuffix(relPath, pattern)
}
