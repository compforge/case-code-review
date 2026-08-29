package language

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func TestTreeSitterFactProgramMatchesLegacyExtractors(t *testing.T) {
	fixtures := []struct {
		language         string
		source           string
		wantGenericFacts bool
		wantHeritage     int
		wantImports      int
	}{
		// gotreesitter also reports the package clause as an ImportRef. CCR's
		// mapper rejects its "package" kind and exposes only the actual import.
		{language: "go", source: "package p\nimport \"fmt\"\nfunc run() { fmt.Println() }\n", wantGenericFacts: true, wantImports: 2},
		{language: "python", source: "import os\nclass Service(Base):\n    def run(self):\n        validate()\n", wantGenericFacts: true, wantHeritage: 1, wantImports: 1},
		{language: "javascript", source: "class Service extends Base { run() { validate(); } }\n", wantGenericFacts: true, wantHeritage: 1},
		{language: "typescript", source: "class Service extends Base { run(): void { validate(); } }\n", wantGenericFacts: true, wantHeritage: 1},
		{language: "tsx", source: "class View extends Base { render() { validate(); return <main />; } }\n", wantGenericFacts: true, wantHeritage: 1},
		{language: "rust", source: "fn run() { validate(); }\n"},
		{language: "java", source: "import example.Base;\nclass Service extends Base implements Runnable { void run() { validate(); } }\n", wantGenericFacts: true, wantHeritage: 2, wantImports: 1},
		{language: "c", source: "void run(void) { validate(); }\n"},
		{language: "cpp", source: "void run() { validate(); }\n"},
	}

	for _, fixture := range fixtures {
		t.Run(fixture.language, func(t *testing.T) {
			entry := grammars.DetectLanguageByName(fixture.language)
			if entry == nil {
				t.Fatalf("gotreesitter has no %q grammar", fixture.language)
			}
			tree := parseTreeSitterFixture(t, *entry, []byte(fixture.source))
			defer tree.Release()

			program, err := treeSitterFactProgram(entry.Language())
			if err != nil {
				t.Fatal(err)
			}
			facts := program.Extract(tree)
			legacyDefinitions := gotreesitter.ExtractDefinitionSpans(tree)
			legacyCalls := gotreesitter.ExtractCalls(tree)
			legacyHeritage := gotreesitter.ExtractHeritage(tree)
			legacyImports := gotreesitter.ExtractImports(tree)
			if !slices.Equal(facts.Definitions, legacyDefinitions) {
				t.Fatalf("definitions differ:\nFactProgram: %#v\nlegacy:      %#v", facts.Definitions, legacyDefinitions)
			}
			if !slices.Equal(facts.Calls, legacyCalls) {
				t.Fatalf("calls differ:\nFactProgram: %#v\nlegacy:      %#v", facts.Calls, legacyCalls)
			}
			if !slices.Equal(facts.Heritage, legacyHeritage) {
				t.Fatalf("heritage differs:\nFactProgram: %#v\nlegacy:      %#v", facts.Heritage, legacyHeritage)
			}
			if !slices.Equal(facts.Imports, legacyImports) {
				t.Fatalf("imports differ:\nFactProgram: %#v\nlegacy:      %#v", facts.Imports, legacyImports)
			}
			if len(facts.Heritage) != fixture.wantHeritage || len(facts.Imports) != fixture.wantImports {
				t.Fatalf("fact coverage = heritage:%d imports:%d, want heritage:%d imports:%d", len(facts.Heritage), len(facts.Imports), fixture.wantHeritage, fixture.wantImports)
			}
			if fixture.language == "go" {
				imports := treeSitterImports(Source{Path: "fixture.go", Content: fixture.source}, facts.Imports)
				if len(imports) != 1 || imports[0].Path != "fmt" {
					t.Fatalf("mapped Go imports = %#v, want only the fmt import", imports)
				}
			}
			if fixture.wantGenericFacts && (len(facts.Definitions) == 0 || len(facts.Calls) == 0) {
				t.Fatalf("fixture did not exercise both fact kinds: %#v", facts)
			}
			if !fixture.wantGenericFacts && (len(facts.Definitions) != 0 || len(facts.Calls) != 0) {
				t.Fatalf("generic extractor coverage changed; review the tags fallback contract: %#v", facts)
			}
		})
	}
}

func TestTreeSitterFactKindsFailClosed(t *testing.T) {
	if _, ok := treeSitterSupertypeKind("mixin"); ok {
		t.Fatal("unknown heritage kind must not become a supertype relation")
	}
	if _, ok := treeSitterImportKind("package"); ok {
		t.Fatal("package declarations must not become imports")
	}
}

func parseTreeSitterFixture(t testing.TB, entry grammars.LangEntry, source []byte) *gotreesitter.Tree {
	t.Helper()
	lang := entry.Language()
	parser := gotreesitter.NewParser(lang)
	var (
		tree *gotreesitter.Tree
		err  error
	)
	if entry.TokenSourceFactory != nil {
		tree, err = parser.ParseWithTokenSourceStrict(source, entry.TokenSourceFactory(source, lang))
	} else {
		tree, err = parser.ParseStrict(source)
	}
	if err != nil {
		t.Fatalf("parse %s fixture: %v", entry.Name, err)
	}
	if tree == nil || tree.RootNode() == nil || tree.RootNode().HasError() {
		if tree != nil {
			tree.Release()
		}
		t.Fatalf("parse %s fixture: incomplete syntax tree", entry.Name)
	}
	return tree
}

var (
	benchmarkTreeSitterDefinitions []gotreesitter.DefinitionSpan
	benchmarkTreeSitterCalls       []gotreesitter.CallRef
	benchmarkTreeSitterHeritage    []gotreesitter.HeritageRef
	benchmarkTreeSitterImports     []gotreesitter.ImportRef
	benchmarkTreeSitterFacts       gotreesitter.FactSet
)

func BenchmarkTreeSitterFactExtraction(b *testing.B) {
	entry := grammars.DetectLanguageByName("java")
	var source strings.Builder
	source.WriteString("import example.Base;\nclass Service extends Base {\n")
	for i := range 1000 {
		fmt.Fprintf(&source, "  void method%d() { dependency%d(); }\n", i, i)
	}
	source.WriteString("}\n")
	tree := parseTreeSitterFixture(b, *entry, []byte(source.String()))
	defer tree.Release()
	program, err := treeSitterFactProgram(entry.Language())
	if err != nil {
		b.Fatal(err)
	}

	b.Run("legacy_four_passes", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			benchmarkTreeSitterDefinitions = gotreesitter.ExtractDefinitionSpans(tree)
			benchmarkTreeSitterCalls = gotreesitter.ExtractCalls(tree)
			benchmarkTreeSitterHeritage = gotreesitter.ExtractHeritage(tree)
			benchmarkTreeSitterImports = gotreesitter.ExtractImports(tree)
		}
	})
	b.Run("fact_program_one_pass", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			benchmarkTreeSitterFacts = program.Extract(tree)
		}
	})
}
