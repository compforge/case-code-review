package codegraph

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeTypedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, src string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module fixture.local/t\n\ngo 1.21\n")
	write("a.go", `package t

func Caller() { Helper() }

func Helper() {}
`)
	return dir
}

func TestTypedGraph_AnswersGoAndFallsBackOtherwise(t *testing.T) {
	tg := &TypedGraph{RepoDir: writeTypedRepo(t)}

	ids, ok := tg.Callers("a.go::Helper")
	if !ok {
		t.Fatal("typed graph should answer for a Go symbol")
	}
	if !slices.Contains(ids, "a.go::Caller") {
		t.Errorf("Callers = %v, want a.go::Caller", ids)
	}

	// Typed empty answer is authoritative for Go symbols.
	if ids, ok := tg.Callers("a.go::Caller"); !ok || len(ids) != 0 {
		t.Errorf("expected authoritative empty for Caller, got %v ok=%v", ids, ok)
	}

	// Non-Go symbols never get a typed answer (ok=false -> grep fallback).
	if _, ok := tg.Callers("app/api.py::handle"); ok {
		t.Error("python symbol must fall back to grep")
	}
}

func TestTypedGraph_NilHandleFallsBack(t *testing.T) {
	var tg *TypedGraph
	if _, ok := tg.Callers("a.go::X"); ok {
		t.Error("nil handle must report ok=false")
	}
	if _, ok := tg.Callees("a.go::X"); ok {
		t.Error("nil handle must report ok=false")
	}
}
