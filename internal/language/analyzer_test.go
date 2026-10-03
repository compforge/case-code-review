package language

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func TestAnalyzeGo(t *testing.T) {
	source := Source{Path: "p.go", Content: `package p

import store "example.com/dependency/v2"

type S struct{}

func Alpha() {
	helper()
	store.Load()
}

func (s *S) Beta() int {
	return s.load()
}
`}
	analysis, err := NewAnalyzer("").Analyze(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	assertDefinition(t, analysis, "p.go::Alpha", Span{Start: 7, End: 10})
	assertDefinition(t, analysis, "p.go::S.Beta", Span{Start: 12, End: 14})
	if definition, ok := analysis.DefinitionAt(13); !ok || definition.SymbolID != "p.go::S.Beta" {
		t.Fatalf("DefinitionAt(13) = (%+v, %v)", definition, ok)
	}
	if symbol, ok := analysis.SymbolAt(5); !ok || symbol.SymbolID != "p.go::S" {
		t.Fatalf("SymbolAt(5) = (%+v, %v)", symbol, ok)
	}
	if symbol, ok := analysis.SymbolAt(13); !ok || symbol.SymbolID != "p.go::S.Beta" {
		t.Fatalf("SymbolAt(13) = (%+v, %v)", symbol, ok)
	}
	assertNames(t, analysis.CalleesOf("S.Beta"), "load")
	assertNames(t, analysis.CalleesOf("p.go::Alpha"), "helper", "Load")
	if len(analysis.SupertypeReferences) != 0 {
		t.Fatalf("supertype references = %#v, Go syntax must not invent inheritance", analysis.SupertypeReferences)
	}
	if len(analysis.Imports) != 1 || analysis.Imports[0].Kind != ImportModule || analysis.Imports[0].Path != "example.com/dependency/v2" || analysis.Imports[0].Alias != "store" || analysis.Imports[0].Span != (Span{Start: 3, End: 3}) {
		t.Fatalf("imports = %#v, want aliased Go dependency", analysis.Imports)
	}
}

func TestAnalyzePython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	source := Source{Path: "p.py", Content: `def alpha():
    helper()

from framework import Base as FrameworkBase

class Svc(FrameworkBase):
    def create(self, req):
        validate(req)
        return self.store(req)
`}
	analysis, err := NewAnalyzer("").Analyze(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	assertDefinition(t, analysis, "p.py::alpha", Span{Start: 1, End: 2})
	assertDefinition(t, analysis, "p.py::Svc.create", Span{Start: 7, End: 9})
	assertNames(t, analysis.CalleesOf("Svc.create"), "validate", "store")
	if len(analysis.SupertypeReferences) != 1 || analysis.SupertypeReferences[0].SubtypeID != "p.py::Svc" || analysis.SupertypeReferences[0].Kind != SupertypeBase || analysis.SupertypeReferences[0].Supertype != "FrameworkBase" || analysis.SupertypeReferences[0].Span != (Span{Start: 6, End: 6}) {
		t.Fatalf("supertype references = %#v, want Svc -> FrameworkBase", analysis.SupertypeReferences)
	}
	if len(analysis.Imports) != 1 || analysis.Imports[0].Kind != ImportFrom || analysis.Imports[0].Path != "framework.Base" || analysis.Imports[0].Alias != "FrameworkBase" || analysis.Imports[0].Span != (Span{Start: 4, End: 4}) {
		t.Fatalf("imports = %#v, want Python from-import", analysis.Imports)
	}
}

func TestAnalyzePythonCapturesRouteDecorators(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	source := Source{Path: "api.py", Content: `
@router.post("/items")
async def create_item():
    return service.create()
`}
	analysis, err := NewAnalyzer("").Analyze(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(analysis.Decorators, "router.post") {
		t.Fatalf("decorators = %v, want router.post", analysis.Decorators)
	}
}

func TestAnalyzeTypeScript(t *testing.T) {
	source := Source{Path: "app.ts", Content: `const helper = () => 1;

class Service extends Base {
  run() {
    return helper() + this.load();
  }
}
`}
	analysis, err := NewAnalyzer("").Analyze(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	assertDefinition(t, analysis, "app.ts::helper", Span{Start: 1, End: 1})
	assertDefinition(t, analysis, "app.ts::Service.run", Span{Start: 4, End: 6})
	assertNames(t, analysis.CalleesOf("Service.run"), "helper", "load")
	if len(analysis.SupertypeReferences) != 1 || analysis.SupertypeReferences[0].SubtypeID != "app.ts::Service" || analysis.SupertypeReferences[0].Kind != SupertypeExtends || analysis.SupertypeReferences[0].Supertype != "Base" || analysis.SupertypeReferences[0].Span != (Span{Start: 3, End: 3}) {
		t.Fatalf("supertype references = %#v, want Service extends Base", analysis.SupertypeReferences)
	}
}

func TestAnalyzeTypeScriptImportTypeQuery(t *testing.T) {
	source := Source{Path: "app.ts", Content: `function load() {
  return factory<typeof import("./model").Result>();
}
`}
	entry := grammars.DetectLanguageByName("typescript")
	parser := gotreesitter.NewParser(entry.Language())
	tree, err := parser.ParseStrict([]byte(source.Content))
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Release()
	if tree.RootNode().HasError() {
		t.Fatal("TypeScript import-type query produced a syntax error")
	}

	analysis, err := NewAnalyzer("").Analyze(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	assertDefinition(t, analysis, "app.ts::load", Span{Start: 1, End: 3})
	assertNames(t, analysis.CalleesOf("load"), "factory")
}

func TestAnalyzeTypeScriptSignedRightShift(t *testing.T) {
	source := []byte(`function shift(a: number, b: number) {
  return a >> (b);
}
`)
	entry := grammars.DetectLanguageByName("typescript")
	parser := gotreesitter.NewParser(entry.Language())
	tree, err := parser.ParseStrict(source)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Release()
	if tree.RootNode().HasError() {
		t.Fatal("TypeScript signed right shift produced a syntax error")
	}
}

func TestAnalyzeUnsupported(t *testing.T) {
	if _, err := NewAnalyzer("").Analyze(context.Background(), Source{Path: "README.unknown-language", Content: "text"}); err == nil {
		t.Fatal("unsupported source must return an error")
	}
}

func TestAnalyzeJavaWithTreeSitter(t *testing.T) {
	source := Source{Path: "Service.java", Content: `import framework.Base;

class Service extends Base implements Runnable, AutoCloseable {
  void run() {
    validate();
  }
}
`}
	analysis, err := NewAnalyzer("").Analyze(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Language != Language("java") || analysis.Quality != QualityPartial {
		t.Fatalf("analysis metadata = (%q, %q)", analysis.Language, analysis.Quality)
	}
	assertDefinition(t, analysis, "Service.java::Service.run", Span{Start: 4, End: 6})
	if len(analysis.Calls) != 0 || len(analysis.SupertypeReferences) != 0 || len(analysis.Imports) != 0 {
		t.Fatalf("generic declaration coverage must not invent semantic facts: %+v", analysis)
	}
}

func TestAnalyzeRustWithTreeSitterTags(t *testing.T) {
	source := Source{Path: "lib.rs", Content: `fn run() {
    validate();
}
`}
	analysis, err := NewAnalyzer("").Analyze(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	assertDefinition(t, analysis, "lib.rs::run", Span{Start: 1, End: 3})
	if analysis.Quality != QualityPartial || len(analysis.Calls) != 0 {
		t.Fatalf("generic language must retain partial coverage: %+v", analysis)
	}
}

func TestStructuredExtensionsIncludeTreeSitterLanguages(t *testing.T) {
	extensions := StructuredExtensions()
	for _, extension := range []string{".go", ".py", ".ts", ".java", ".rs"} {
		if !slices.Contains(extensions, extension) {
			t.Fatalf("StructuredExtensions missing %q", extension)
		}
	}
	for _, extension := range []string{".json", ".yaml", ".md", ".csv"} {
		if slices.Contains(extensions, extension) {
			t.Fatalf("StructuredExtensions includes file-scope suffix %q", extension)
		}
	}
	if len(extensions) >= len(grammars.AllLanguages()) {
		t.Fatalf("StructuredExtensions should be bounded by ccr's review set, got %d", len(extensions))
	}
}

func TestDetectUsesCodeGraphLanguageSelection(t *testing.T) {
	tests := []struct {
		path string
		want Language
	}{
		{path: "Program.fs", want: Language("forth")},
		{path: "Controller.m", want: Language("matlab")},
	}
	for _, tt := range tests {
		if got, ok := Detect(tt.path); !ok || got != tt.want {
			t.Errorf("Detect(%q) = (%q, %v), want (%q, true)", tt.path, got, ok, tt.want)
		}
	}
}

func TestSignatureDisplayIsBoundedAndUTF8Safe(t *testing.T) {
	content := "const render = () => " + strings.Repeat("界", 300)
	got := displaySignature(content)
	if !utf8.ValidString(got) {
		t.Fatalf("signature is not valid UTF-8: %q", got)
	}
	if len([]rune(got)) > 240 || !strings.HasSuffix(got, "...") {
		t.Fatalf("signature length = %d, signature = %q", len([]rune(got)), got)
	}
}

func assertDefinition(t *testing.T, analysis Analysis, id string, want Span) {
	t.Helper()
	definition, ok := analysis.DefinitionByID(id)
	if !ok {
		t.Fatalf("definition %q missing from %+v", id, analysis.Definitions)
	}
	if definition.Span != want {
		t.Fatalf("%s span = %+v, want %+v", id, definition.Span, want)
	}
}

func assertNames(t *testing.T, got []string, want ...string) {
	t.Helper()
	set := map[string]bool{}
	for _, name := range got {
		set[name] = true
	}
	for _, name := range want {
		if !set[name] {
			t.Fatalf("missing %q in %v", name, got)
		}
	}
}
