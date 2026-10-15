package project

import (
	"context"
	"github.com/compforge/repocli/toolkit/go"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func repositoryWith(t *testing.T, files ...string) *Repository {
	t.Helper()
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("init: %s: %v", out, err)
	}
	for _, file := range files {
		name := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
			t.Fatal(err)
		}
		content := ""
		if filepath.Base(file) == "package.json" {
			content = `{"devDependencies":{"typescript":"*"}}`
		}
		if err := os.WriteFile(name, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	report, err := repocli.Inspect(context.Background(), repocli.InputRequest{Repository: root})
	if err != nil {
		t.Fatal(err)
	}
	return NewRepository(report.Layout)
}

func TestRepositoryClassifiesNearestComponent(t *testing.T) {
	r := repositoryWith(t, "backend/pyproject.toml", "go.mod")
	tests := []struct {
		path  string
		root  string
		kind  Kind
		roles FileRoles
		ok    bool
	}{
		{"backend/app/service.py", "backend", Python, FileRoles{RoleSource}, true},
		{"backend/routers/users.py", "backend", Python, FileRoles{RoleSource}, true},
		{"backend/app/__main__.py", "backend", Python, FileRoles{RoleSource, RoleEntrypoint}, true},
		{"backend/pyproject.toml", "backend", Python, FileRoles{RoleManifest}, true},
		{"backend/uv.lock", "backend", Python, FileRoles{RoleLock}, true},
		{"cmd/ccr/main.go", ".", Go, FileRoles{RoleSource, RoleEntrypoint}, true},
		{"internal/project/repository_test.go", ".", Go, FileRoles{RoleSource, RoleTest}, true},
		{"go.mod", ".", Go, FileRoles{RoleManifest}, true},
		{"go.sum", ".", Go, FileRoles{RoleLock}, true},
		{"VERSION", ".", Go, FileRoles{RoleVersion}, true},
		{"docs/VERSION", ".", Go, FileRoles{RoleUnknown}, true},
		{"README.md", ".", Go, FileRoles{RoleUnknown}, true},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, roles, ok := r.Resolve(tt.path)
			if ok != tt.ok || got.Root != tt.root || got.Kind != tt.kind || roles.String() != tt.roles.String() {
				t.Fatalf("Resolve(%q) = (%+v, %q, %t), want root=%q kind=%q roles=%q ok=%t",
					tt.path, got, roles, ok, tt.root, tt.kind, tt.roles, tt.ok)
			}
		})
	}
}

func TestPythonSemanticRolesRequireSourceFacts(t *testing.T) {
	roles := EnrichFileRoles(
		"backend/api/v1/endpoints/items.py",
		FileRoles{RoleSource},
		[]string{"router.post"},
		nil,
	)
	if !roles.Has(RoleHandler) {
		t.Fatalf("decorated endpoint roles = %v, want handler", roles)
	}

	plain := EnrichFileRoles(
		"backend/api/v1/routes.py",
		FileRoles{RoleSource},
		nil,
		[]string{"APIRouter", "include_router"},
	)
	if plain.Has(RoleHandler) {
		t.Fatalf("router aggregator roles = %v, must not be handler", plain)
	}

	main := EnrichFileRoles(
		"backend/main.py",
		FileRoles{RoleSource},
		[]string{"app.get"},
		[]string{"FastAPI"},
	)
	if !main.Has(RoleEntrypoint) || !main.Has(RoleHandler) {
		t.Fatalf("FastAPI main roles = %v, want entrypoint + handler", main)
	}

	apiRoute := EnrichFileRoles(
		"backend/api/v1/endpoints/items.py",
		FileRoles{RoleSource},
		[]string{"router.api_route"},
		nil,
	)
	if !apiRoute.Has(RoleHandler) {
		t.Fatalf("api_route endpoint roles = %v, want handler", apiRoute)
	}
}

func TestRepositoryKeepsOnePolyglotEngineeringComponent(t *testing.T) {
	r := repositoryWith(t, "go.mod", "pyproject.toml", "package.json")

	if got, roles, ok := r.Resolve("config.yaml"); !ok || got.Root != "." || roles.Known() {
		t.Fatalf("polyglot ownership/role = %+v %v %v", got, roles, ok)
	}
	if got, roles, ok := r.Resolve("main.go"); !ok || got.Kind != Python || !roles.Has(RoleSource) || !roles.Has(RoleEntrypoint) {
		t.Fatalf("Go source = (%+v, %q, %t)", got, roles, ok)
	}
	if got, roles, ok := r.Resolve("main.py"); !ok || got.Kind != Python || !roles.Has(RoleSource) {
		t.Fatalf("Python source = (%+v, %q, %t)", got, roles, ok)
	}
	if got, roles, ok := r.Resolve("main.ts"); !ok || got.Kind != Python || !roles.Has(RoleSource) || !roles.Has(RoleEntrypoint) {
		t.Fatalf("TypeScript source = (%+v, %q, %t)", got, roles, ok)
	}
}

func TestRepositoryClassifiesTypeScriptComponents(t *testing.T) {
	// Mirrors Doctor (TypeScript CLI + Python server) and Baton's nested
	// package workspace without teaching Project either repository by name.
	r := repositoryWith(t,
		"cli/package.json", "server/pyproject.toml", "server/harness/pyproject.toml",
		"package.json", "packages/plugin/package.json",
	)
	tests := []struct {
		path  string
		root  string
		kind  Kind
		roles FileRoles
	}{
		{"cli/src/app/main.tsx", "cli", TypeScript, FileRoles{RoleSource, RoleEntrypoint}},
		{"cli/src/app/main.spec.tsx", "cli", TypeScript, FileRoles{RoleSource, RoleTest}},
		{"cli/package.json", "cli", TypeScript, FileRoles{RoleManifest}},
		{"cli/tsconfig.json", "cli", TypeScript, FileRoles{RoleManifest}},
		{"cli/bun.lock", "cli", TypeScript, FileRoles{RoleLock}},
		{"server/app.py", "server", Python, FileRoles{RoleSource}},
		{"server/tests/test_app.py", "server", Python, FileRoles{RoleSource, RoleTest}},
		{"server/conftest.py", "server", Python, FileRoles{RoleSource, RoleTest}},
		{"server/harness/runtime.py", "server", Python, FileRoles{RoleSource}},
		{"src/index.ts", ".", TypeScript, FileRoles{RoleSource}},
		{"src/cli/launcher.cjs", ".", TypeScript, FileRoles{RoleSource}},
		{"packages/plugin/src/index.ts", "packages/plugin", TypeScript, FileRoles{RoleSource}},
		{"VERSION", ".", TypeScript, FileRoles{RoleVersion}},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, roles, ok := r.Resolve(tt.path)
			if !ok || got.Root != tt.root || got.Kind != tt.kind || roles.String() != tt.roles.String() {
				t.Fatalf("Resolve(%q) = (%+v, %q, %t), want root=%q kind=%q roles=%q",
					tt.path, got, roles, ok, tt.root, tt.kind, tt.roles)
			}
		})
	}
}

func TestRepositoryLeavesRepositoryFileUnowned(t *testing.T) {
	r := repositoryWith(t, "backend/pyproject.toml")
	if got, roles, ok := r.Resolve("README.md"); ok {
		t.Fatalf("repository README unexpectedly owned by %+v as %q", got, roles)
	}
}
