package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"github.com/qiankunli/case-code-review/internal/gitcmd"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// This signature identifies captured changes before selection/grouping. It is
// not a digest of every context read or a claim that two reviews are equivalent.
func changeDigest(changes []change.Change) string {
	ordered := slices.Clone(changes)
	slices.SortFunc(ordered, func(a, b change.Change) int { return strings.Compare(a.Path(), b.Path()) })
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	for _, item := range ordered {
		_ = encoder.Encode(item)
	} // Change contains only JSON scalar fields.
	return hex.EncodeToString(hash.Sum(nil))
}

func (a *Runner) persistReviewInput(ctx context.Context) {
	git := a.args.GitRunner
	if git == nil {
		git = gitcmd.New(0)
	}
	// Worktrees share this identity. Failure leaves identity unknown rather
	// than treating equal filenames from unrelated repositories as comparable.
	repository := ""
	if data, err := git.Output(ctx, a.args.RepoDir, "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
		repository, _ = filepath.EvalSymlinks(strings.TrimSpace(string(data)))
	}
	a.session.WriteArtifact("review_input", map[string]any{
		"version": 1, "repository": repository,
		"change_digest": changeDigest(a.changes),
	})
}
