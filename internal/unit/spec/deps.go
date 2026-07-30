package spec

import (
	"os"
	"path/filepath"

	"github.com/qiankunli/case-code-review/internal/language"
)

// loadDepSpecs discovers spec.json shipped inside installed dependencies
// (Model A: spec travels with the package) and returns entries keyed by fqn —
// the only identity meaningful outside the dependency's own repository.
// Language owns ecosystem-specific dependency discovery; spec only consumes
// its own asset from the returned package roots.
func loadDepSpecs(repoDir string) map[string]Entry {
	if repoDir == "" {
		return nil
	}
	var out map[string]Entry
	for _, root := range language.DependencyRoots(repoDir) {
		data, err := os.ReadFile(filepath.Join(root, "spec.json"))
		if err != nil {
			continue
		}
		idx, err := Parse(data)
		if err != nil {
			continue
		}
		for _, entry := range idx {
			if entry.Fqn == "" {
				continue
			}
			if out == nil {
				out = map[string]Entry{}
			}
			out[entry.Fqn] = entry
		}
	}
	return out
}
