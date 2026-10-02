// Package language adapts CodeGraph source facts and supplies repository inputs. Review concepts such as units, ranking, and clue traversal consume
// these facts but remain in their own packages.
package language

import (
	"sort"

	cg "github.com/compforge/codegraph"
)

// Data and markup files remain reviewable at file scope, but should not enter
// function-oriented repository scans or callgraph grep pathspecs.
var fileScopeExtensions = map[string]bool{
	".css": true, ".scss": true, ".sass": true, ".less": true,
	".html": true, ".htm": true, ".xml": true,
	".yaml": true, ".yml": true, ".json": true, ".json5": true,
	".toml": true, ".ini": true, ".env": true,
}

// Language identifies the grammar used to analyze a source file.
type Language string

const (
	Go         Language = "go"
	Python     Language = "python"
	TypeScript Language = "typescript"
	TSX        Language = "tsx"
	JavaScript Language = "javascript"
	JSX        Language = "jsx"
)

// Detect identifies a language with a structured-analysis backend from a
// source path. Reviewable languages without a backend still degrade to file
// scope at the unit boundary.
func Detect(path string) (Language, bool) {
	name := cg.Language(path)
	return Language(name), name != ""
}

// StructuredExtensions returns the source suffixes currently backed by
// Analyzer. Consumers may use them to bound source discovery without learning
// which parser implements each language.
func StructuredExtensions() []string {
	var extensions []string
	for _, extension := range ReviewableExtensions() {
		if fileScopeExtensions[extension] {
			continue
		}
		if _, ok := Detect("source" + extension); ok {
			extensions = append(extensions, extension)
		}
	}
	sort.Strings(extensions)
	return extensions
}

// Source is one repository-relative source file analyzed at its current
// contents. RepoDir is kept on Analyzer because project-local tooling is a
// property of the analysis session, not of an individual file.
type Source struct {
	Path    string
	Content string
}

// Kind is the source-level role of a definition.
type Kind string

const (
	KindVariable  Kind = "variable"
	KindFunction  Kind = "function"
	KindMethod    Kind = "method"
	KindClass     Kind = "class"
	KindType      Kind = "type"
	KindInterface Kind = "interface"
)

// Span is a 1-indexed inclusive source-line range.
type Span struct {
	Start int
	End   int
}

// Definition is a named source declaration. SymbolID is the stable cross-
// feature join key; Name is its language-level symbol (for example Svc.Run).
type Definition struct {
	SymbolID  string
	Name      string
	Owner     string
	Kind      Kind
	Span      Span
	Signature string
}

// Callable reports whether a definition can own calls and a function review
// unit. Types/classes remain useful to repository maps but do not split diffs.
func (d Definition) Callable() bool {
	return d.Kind == KindFunction || d.Kind == KindMethod
}

// Call is a syntactic call made inside a definition. Name is deliberately
// unresolved; semantic backends may additionally provide resolved call edges.
type Call struct {
	CallerID string
	Name     string
}

// SupertypeReference is one explicit source-level reference from a declared
// subtype to a supertype. Supertype remains unresolved: syntax backends can
// prove the declaration but not necessarily which repository symbol the name
// denotes. Span locates the referenced supertype, not the whole declaration.
// A graph resolver can combine this fact with imports and repository definitions
// while preserving the precise extends, implements, or base relation.
type SupertypeReference struct {
	SubtypeID string
	Kind      SupertypeKind
	Supertype string
	Span      Span
}

type SupertypeKind string

const (
	SupertypeExtends    SupertypeKind = "extends"
	SupertypeImplements SupertypeKind = "implements"
	SupertypeBase       SupertypeKind = "base"
)

// Import is one explicit dependency declaration. Path is the best available
// module/package address; From and Relative preserve Python-style import
// structure without leaking a parser-specific representation. Span locates the
// source import entry. Imports are name-resolution facts, not Unit relations.
type Import struct {
	Kind     ImportKind
	Path     string
	From     string
	Name     string
	Alias    string
	Static   bool
	Wildcard bool
	Relative int
	Span     Span
}

type ImportKind string

const (
	ImportModule ImportKind = "import"
	ImportFrom   ImportKind = "from_import"
	ImportLoad   ImportKind = "load"
)

// Quality describes how trustworthy the returned language facts are.
type Quality string

const (
	QualitySyntax  Quality = "syntax"
	QualityPartial Quality = "partial"
)

// Analysis is the parser-independent fact model consumed by ccr. Parser trees,
// query captures, and backend-specific nodes must never cross this boundary.
type Analysis struct {
	Language            Language
	Quality             Quality
	Definitions         []Definition
	Calls               []Call
	SupertypeReferences []SupertypeReference
	Imports             []Import
	Decorators          []string
	References          map[string]int
	outlineEntries      []outlineEntry
}

// DefinitionAt returns the innermost callable definition containing line.
func (a Analysis) DefinitionAt(line int) (Definition, bool) {
	return a.definitionAt(line, true)
}

// SymbolAt returns the innermost named definition containing line. Unlike
// DefinitionAt it includes types and classes, which makes it suitable for
// bounded source navigation without changing callable-based Unit formation.
func (a Analysis) SymbolAt(line int) (Definition, bool) {
	return a.definitionAt(line, false)
}

func (a Analysis) definitionAt(line int, callableOnly bool) (Definition, bool) {
	var best Definition
	found := false
	for _, d := range a.Definitions {
		if (callableOnly && !d.Callable()) || line < d.Span.Start || line > d.Span.End {
			continue
		}
		spanLines := d.Span.End - d.Span.Start
		bestSpanLines := best.Span.End - best.Span.Start
		if !found || spanLines < bestSpanLines ||
			(spanLines == bestSpanLines && d.Callable() && !best.Callable()) {
			best, found = d, true
		}
	}
	return best, found
}

// DefinitionByID returns the definition with the canonical symbol id.
func (a Analysis) DefinitionByID(id string) (Definition, bool) {
	for _, d := range a.Definitions {
		if d.SymbolID == id {
			return d, true
		}
	}
	return Definition{}, false
}

// CalleesOf returns the distinct unresolved call names made by symbol.
func (a Analysis) CalleesOf(symbol string) []string {
	_, target, ok := SplitSymbolID(symbol)
	if !ok {
		target = symbol
	}
	seen := map[string]bool{}
	var names []string
	for _, call := range a.Calls {
		_, caller, callerOK := SplitSymbolID(call.CallerID)
		if !callerOK {
			caller = call.CallerID
		}
		if caller != target || call.Name == "" || seen[call.Name] {
			continue
		}
		seen[call.Name] = true
		names = append(names, call.Name)
	}
	if len(names) == 0 {
		return nil
	}
	return names
}
