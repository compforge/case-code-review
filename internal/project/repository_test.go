package project

import "testing"

func repositoryWith(files ...string) *Repository {
	present := make(map[string]bool, len(files))
	for _, file := range files {
		present[file] = true
	}
	return NewRepository("/repo", func(path string) bool { return present[path] })
}

func TestRepositoryClassifiesNearestComponent(t *testing.T) {
	r := repositoryWith("backend/pyproject.toml", "go.mod")
	if r.Root != "/repo" {
		t.Fatalf("repository root = %q, want /repo", r.Root)
	}

	tests := []struct {
		path string
		root string
		kind Kind
		role FileRole
		ok   bool
	}{
		{"backend/app/service.py", "backend", Python, RoleSource, true},
		{"backend/pyproject.toml", "backend", Python, RoleManifest, true},
		{"backend/uv.lock", "backend", Python, RoleLock, true},
		{"cmd/ccr/main.go", ".", Go, RoleSource, true},
		{"go.mod", ".", Go, RoleManifest, true},
		{"go.sum", ".", Go, RoleLock, true},
		{"README.md", ".", Go, RoleUnknown, true},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, role, ok := r.Resolve(tt.path)
			if ok != tt.ok || got.Root != tt.root || got.Kind != tt.kind || role != tt.role {
				t.Fatalf("Resolve(%q) = (%+v, %q, %t), want root=%q kind=%q role=%q ok=%t",
					tt.path, got, role, ok, tt.root, tt.kind, tt.role, tt.ok)
			}
		})
	}
}

func TestRepositoryDoesNotGuessBetweenPolyglotComponents(t *testing.T) {
	r := repositoryWith("go.mod", "pyproject.toml")

	if got, role, ok := r.Resolve("config.yaml"); ok {
		t.Fatalf("ambiguous config unexpectedly owned by %+v as %q", got, role)
	}
	if got, role, ok := r.Resolve("main.go"); !ok || got.Kind != Go || role != RoleSource {
		t.Fatalf("Go source = (%+v, %q, %t)", got, role, ok)
	}
	if got, role, ok := r.Resolve("main.py"); !ok || got.Kind != Python || role != RoleSource {
		t.Fatalf("Python source = (%+v, %q, %t)", got, role, ok)
	}
}

func TestRepositoryLeavesRepositoryFileUnowned(t *testing.T) {
	r := repositoryWith("backend/pyproject.toml")
	if got, role, ok := r.Resolve("README.md"); ok {
		t.Fatalf("repository README unexpectedly owned by %+v as %q", got, role)
	}
}
