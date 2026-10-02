package language

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReferencesUseCodeGraphImportBindings(t *testing.T) {
	source := `package x
import (
    "github.com/org/framework/mw/trace"
    tr "github.com/org/other/trace"
    _ "embed"
    . "fmt"
    lib "github.com/org/lib/v2"
)
import "strings"
`
	references := NewAnalyzer("").ReferencesIn(Source{Path: "source.go", Content: source}, "trace.Load() tr.Load() lib.Load() strings.Load()")
	for _, path := range []string{"github.com/org/framework/mw/trace", "github.com/org/other/trace", "github.com/org/lib/v2", "strings"} {
		assertReference(t, references, Reference{Name: "Load", FQN: path + ".Load"})
	}
}

func TestReferencesInGo(t *testing.T) {
	source := Source{Path: "handler.go", Content: `package x
import tr "github.com/org/trace"
`}
	references := NewAnalyzer("").ReferencesIn(source, "+ tr.Middleware(NewHandler)\n")
	assertReference(t, references, Reference{Name: "Middleware", FQN: "github.com/org/trace.Middleware"})
	assertReference(t, references, Reference{Name: "NewHandler"})
}

func TestReferencesInPython(t *testing.T) {
	repo := t.TempDir()
	module := filepath.Join(repo, "framework", "trace.py")
	if err := os.MkdirAll(filepath.Dir(module), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(module, []byte("class Middleware: pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	source := Source{Path: "handler.py", Content: "from framework.trace import Middleware as MW\n"}
	references := NewAnalyzer(repo).ReferencesIn(source, "+ return MW(request)\n")
	assertReference(t, references, Reference{
		Name: "MW", FQN: "framework.trace.Middleware", SourcePath: module, SourceName: "Middleware",
	})
	assertReference(t, references, Reference{Name: "request"})
}

func assertReference(t *testing.T, references []Reference, want Reference) {
	t.Helper()
	for _, reference := range references {
		if reference == want {
			return
		}
	}
	t.Fatalf("reference %+v missing from %+v", want, references)
}
