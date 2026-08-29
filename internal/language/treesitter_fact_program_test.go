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
	}{
		{language: "go", source: "package p\nfunc run() { validate() }\n", wantGenericFacts: true},
		{language: "python", source: "def run():\n    validate()\n", wantGenericFacts: true},
		{language: "javascript", source: "function run() { validate(); }\n", wantGenericFacts: true},
		{language: "typescript", source: "function run(): void { validate(); }\n", wantGenericFacts: true},
		{language: "tsx", source: "function View() { validate(); return <main />; }\n", wantGenericFacts: true},
		{language: "rust", source: "fn run() { validate(); }\n"},
		{language: "java", source: "class Service { void run() { validate(); } }\n", wantGenericFacts: true},
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
			if !slices.Equal(facts.Definitions, legacyDefinitions) {
				t.Fatalf("definitions differ:\nFactProgram: %#v\nlegacy:      %#v", facts.Definitions, legacyDefinitions)
			}
			if !slices.Equal(facts.Calls, legacyCalls) {
				t.Fatalf("calls differ:\nFactProgram: %#v\nlegacy:      %#v", facts.Calls, legacyCalls)
			}
			if fixture.wantGenericFacts && (len(facts.Definitions) == 0 || len(facts.Calls) == 0) {
				t.Fatalf("fixture did not exercise both fact kinds: %#v", facts)
			}
			if !fixture.wantGenericFacts && (len(facts.Definitions) != 0 || len(facts.Calls) != 0) {
				t.Fatalf("generic extractor coverage changed; review the tags fallback contract: %#v", facts)
			}
			if facts.Heritage != nil || facts.Imports != nil {
				t.Fatalf("unrequested facts were extracted: %#v", facts)
			}
		})
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
	benchmarkTreeSitterFacts       gotreesitter.FactSet
)

func BenchmarkTreeSitterFactExtraction(b *testing.B) {
	entry := grammars.DetectLanguageByName("java")
	var source strings.Builder
	source.WriteString("class Service {\n")
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

	b.Run("legacy_two_passes", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			benchmarkTreeSitterDefinitions = gotreesitter.ExtractDefinitionSpans(tree)
			benchmarkTreeSitterCalls = gotreesitter.ExtractCalls(tree)
		}
	})
	b.Run("fact_program_one_pass", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			benchmarkTreeSitterFacts = program.Extract(tree)
		}
	})
}
