package spec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDepSpecs_GoModCache(t *testing.T) {
	repo := t.TempDir()
	cache := t.TempDir()
	t.Setenv("GOMODCACHE", cache)

	// repo requires a dependency that ships a spec.json (with an uppercase letter
	// in the path, to exercise escaping).
	os.WriteFile(filepath.Join(repo, "go.mod"),
		[]byte("module github.com/org/app\n\ngo 1.25\n\nrequire github.com/org/Framework v1.2.3\n"), 0o644)

	depDir := filepath.Join(cache, "github.com/org/!framework@v1.2.3")
	if err := os.MkdirAll(depDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(depDir, "spec.json"),
		[]byte(`{"common/middleware/trace.go::PhaseEventMiddleware":{"fqn":"github.com/org/Framework/common/middleware/trace.PhaseEventMiddleware","cases":[],"rules":["per-request only"]}}`), 0o644)

	deps := loadDepSpecs(repo)
	// dependency entries are keyed by fqn — their relpath keys belong to the
	// dependency's own tree and never join the consumer's address space.
	e, ok := deps["github.com/org/Framework/common/middleware/trace.PhaseEventMiddleware"]
	if !ok {
		t.Fatalf("dep spec not discovered/merged; got %v", keys(deps))
	}
	if len(e.Rules) != 1 {
		t.Errorf("entry missing rules: %+v", e)
	}
}

func TestLoadDepSpecs_PythonVenv(t *testing.T) {
	repo := t.TempDir()
	sp := filepath.Join(repo, ".venv", "lib", "python3.11", "site-packages", "framework")
	if err := os.MkdirAll(sp, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(sp, "spec.json"),
		[]byte(`{"framework/mw/trace.py::PhaseEventMiddleware":{"fqn":"framework.mw.trace.PhaseEventMiddleware","cases":[],"rules":["per-request only"]}}`), 0o644)

	deps := loadDepSpecs(repo)
	if _, ok := deps["framework.mw.trace.PhaseEventMiddleware"]; !ok {
		t.Fatalf("python venv dep spec not discovered; got %v", keys(deps))
	}
}

func keys(m map[string]Entry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
