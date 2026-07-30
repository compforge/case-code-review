package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/unit"
)

var allGates = KindGates{Spec: true, Rule: true, Link: true, Doc: true}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEnclosingSymbol(t *testing.T) {
	cases := []struct {
		id, want string
		ok       bool
	}{
		{"api.py::Svc.get", "api.py::Svc", true},
		{"handler.go::Service.CreateNotebook", "handler.go::Service", true},
		{"api.py::Outer.Inner.method", "api.py::Outer.Inner", true}, // immediate owner
		{"api.py::top_level", "", false},                            // no dot → no owner
		{"noscope", "", false},                                      // no ::
	}
	for _, c := range cases {
		got, ok := language.EnclosingSymbolID(c.id)
		if got != c.want || ok != c.ok {
			t.Errorf("enclosingSymbol(%q) = (%q,%v), want (%q,%v)", c.id, got, ok, c.want, c.ok)
		}
	}
}

func TestRelatedFinder_SelfMarks(t *testing.T) {
	idx, err := Parse([]byte(`{
	  "a.go::Foo": {
	    "spec": "must stay tenant-scoped",
	    "cases": [{ "id": "happy", "desc": "ok", "expect": "200" }],
	    "rules": ["hot path; watch new sync DB calls"],
	    "links": ["docs/x.md", "a.go::Bar"]
	  }
	}`))
	if err != nil {
		t.Fatal(err)
	}
	u := unit.UnitOf(unit.Fragment{Path: "a.go", Symbols: []string{"a.go::Foo"}})
	clues := NewRelatedFinder(Catalog{Local: idx}, "", allGates).Find(u)

	byKind := map[unit.ClueKind][]unit.Clue{}
	for _, c := range clues {
		if c.Relation != unit.RelSelf {
			t.Errorf("expected only self clues here, got %+v", c)
		}
		byKind[c.Kind] = append(byKind[c.Kind], c)
	}
	if s := byKind[unit.ClueSpec]; len(s) != 1 || !strings.Contains(s[0].Text, "tenant-scoped") {
		t.Errorf("self spec: %+v", s)
	}
	if r := byKind[unit.ClueRule]; len(r) != 1 || r[0].Text != "hot path; watch new sync DB calls" {
		t.Errorf("self rule: %+v", r)
	}
	if l := byKind[unit.ClueLink]; len(l) != 2 || l[0].Ref != "docs/x.md" || l[0].Text != "docs/x.md (doc)" ||
		l[1].Text != "a.go::Bar (function)" {
		t.Errorf("self links should label doc vs function and keep Ref: %+v", l)
	}
}

// A kind gate switches its evidence kind off across EVERY relation — the
// ablation unit matches the relation×kind matrix.
func TestRelatedFinder_KindGatesAreKindWide(t *testing.T) {
	idx, err := Parse([]byte(`{
	  "trace.py::Svc": { "spec": "type contract", "cases": [], "rules": ["type-wide rule"] },
	  "trace.py::Svc.get": { "spec": "self spec", "cases": [], "rules": ["self rule"], "links": ["docs/x.md"] }
	}`))
	if err != nil {
		t.Fatal(err)
	}
	u := unit.UnitOf(unit.Fragment{Path: "trace.py", Symbols: []string{"trace.py::Svc.get"}})

	// all kinds off → nothing, regardless of relation (owner rule included).
	if got := NewRelatedFinder(Catalog{Local: idx}, "", KindGates{}).Find(u); got != nil {
		t.Errorf("all kinds gated off should find nothing, got %+v", got)
	}

	// rule kind alone → self rule + owner rule, but no spec of either relation.
	clues := NewRelatedFinder(Catalog{Local: idx}, "", KindGates{Rule: true}).Find(u)
	var selfRule, ownerRule, anySpec bool
	for _, c := range clues {
		switch {
		case c.Kind == unit.ClueRule && c.Relation == unit.RelSelf:
			selfRule = true
		case c.Kind == unit.ClueRule && c.Relation == unit.RelOwner:
			ownerRule = true
		case c.Kind == unit.ClueSpec:
			anySpec = true
		}
	}
	if !selfRule || !ownerRule || anySpec {
		t.Errorf("rule-only gates: want self+owner rules and no spec, got %+v", clues)
	}
}

// The doc kind gate silences derived docstrings (here: the owner's).
func TestRelatedFinder_DocGate(t *testing.T) {
	repo := t.TempDir()
	write(t, filepath.Join(repo, "trace.py"),
		"class Svc:\n    \"\"\"Type contract.\"\"\"\n\n    def get(self):\n        ...\n")
	u := unit.UnitOf(unit.Fragment{Path: "trace.py", Symbols: []string{"trace.py::Svc.get"}})
	if got := NewRelatedFinder(Catalog{}, repo, KindGates{Spec: true, Rule: true, Link: true}).Find(u); got != nil {
		t.Errorf("doc gated off should silence docstrings, got %+v", got)
	}
}

func TestRelatedFinder_NilIndexSafe(t *testing.T) {
	u := unit.UnitOf(unit.Fragment{Path: "x", Symbols: []string{"x::Unknown"}})
	if got := NewRelatedFinder(Catalog{}, "", allGates).Find(u); got != nil {
		t.Errorf("nil index should find nothing, got %+v", got)
	}
}

func TestRelatedFinder_OwnerMarks(t *testing.T) {
	idx, err := Parse([]byte(`{
	  "trace.py::PhaseEventMiddleware": { "spec": "per-request lifecycle", "cases": [], "rules": ["per-request only — do not cache"], "links": ["docs/mw.md"] }
	}`))
	if err != nil {
		t.Fatal(err)
	}
	// changing a *method* of PhaseEventMiddleware surfaces the class's markers
	u := unit.UnitOf(unit.Fragment{Path: "trace.py", Symbols: []string{"trace.py::PhaseEventMiddleware.dispatch"}})
	clues := NewRelatedFinder(Catalog{Local: idx}, "", allGates).Find(u)

	var rule, spec *unit.Clue
	for _, c := range clues {
		if c.Relation != unit.RelOwner {
			continue
		}
		switch c.Kind {
		case unit.ClueRule:
			rule = &c
		case unit.ClueSpec:
			spec = &c
		}
	}
	if spec == nil || !strings.Contains(spec.Text, "per-request lifecycle") {
		t.Errorf("want enclosing spec, got clues %+v", clues)
	}
	// Text is raw; the identity travels in Ref (render labels from it).
	if rule == nil || rule.Text != "per-request only — do not cache" || rule.Ref != "trace.py::PhaseEventMiddleware" {
		t.Errorf("want raw enclosing rule with owner symbol-id in Ref, got %+v", rule)
	}

	// when the class itself is the changed symbol there is no owner (top-level).
	self := unit.UnitOf(unit.Fragment{Path: "trace.py", Symbols: []string{"trace.py::PhaseEventMiddleware"}})
	for _, c := range NewRelatedFinder(Catalog{Local: idx}, "", allGates).Find(self) {
		if c.Relation == unit.RelOwner {
			t.Errorf("top-level symbol has no owner, got %+v", c)
		}
	}
}

func TestRelatedFinder_OwnerDocstring(t *testing.T) {
	repo := t.TempDir()
	write(t, filepath.Join(repo, "trace.py"),
		"class PhaseEventMiddleware:\n    \"\"\"Per-request only — do not reuse across requests.\"\"\"\n\n    def dispatch(self):\n        ...\n")

	// no spec.json markers at all — docstring is the only (adoption-free) context.
	u := unit.UnitOf(unit.Fragment{Path: "trace.py", Symbols: []string{"trace.py::PhaseEventMiddleware.dispatch"}})
	clues := NewRelatedFinder(Catalog{}, repo, allGates).Find(u)

	if len(clues) != 1 || clues[0].Kind != unit.ClueDoc || clues[0].Relation != unit.RelOwner ||
		clues[0].Ref != "trace.py::PhaseEventMiddleware" ||
		!strings.Contains(clues[0].Text, "Per-request only") {
		t.Fatalf("want one owner-relation doc clue from the enclosing class docstring, got %+v", clues)
	}
}

func TestRelatedFinder_UsedRule(t *testing.T) {
	idx, err := Parse([]byte(`{
	  "mw/trace.go::PhaseEventMiddleware": { "cases": [], "rules": ["per-request only — do not cache/reuse"] },
	  "handler.go::NewHandler": { "cases": [], "rules": ["own rule"] }
	}`))
	if err != nil {
		t.Fatal(err)
	}
	rf := NewRelatedFinder(Catalog{Local: idx}, "", allGates)

	// A unit that USES PhaseEventMiddleware picks up its class rule, even though
	// the middleware's own definition isn't in this diff.
	u := unit.UnitOf(unit.Fragment{
		Path:    "handler.go",
		Symbols: []string{"handler.go::NewHandler"},
		Diff:    "+\tmw := PhaseEventMiddleware()\n",
	})
	var used []unit.Clue
	for _, c := range rf.Find(u) {
		if c.Relation == unit.RelUsed {
			used = append(used, c)
		}
	}
	if len(used) != 1 || used[0].Kind != unit.ClueRule || used[0].Ref != "PhaseEventMiddleware" ||
		!strings.Contains(used[0].Text, "per-request only") {
		t.Fatalf("want one used-relation rule clue, got %+v", used)
	}

	// The unit's OWN symbol appearing in its own diff must not self-inject via used.
	own := unit.UnitOf(unit.Fragment{
		Path:    "handler.go",
		Symbols: []string{"handler.go::NewHandler"},
		Diff:    "+func NewHandler() {}\n",
	})
	for _, c := range rf.Find(own) {
		if c.Relation == unit.RelUsed {
			t.Errorf("own symbol should not self-inject, got %+v", c)
		}
	}
}

// A used symbol's authored spec is a contract on this change too (higher signal
// than its docstring) — injected alongside its rules, labelled the same way.
func TestRelatedFinder_UsedSpec(t *testing.T) {
	idx, err := Parse([]byte(`{
	  "mw/trace.go::PhaseEventMiddleware": { "spec": "accumulates one request's phase events", "cases": [] }
	}`))
	if err != nil {
		t.Fatal(err)
	}
	u := unit.UnitOf(unit.Fragment{
		Path:    "handler.go",
		Symbols: []string{"handler.go::NewHandler"},
		Diff:    "+\tmw := PhaseEventMiddleware()\n",
	})
	clues := NewRelatedFinder(Catalog{Local: idx}, "", allGates).Find(u)
	if len(clues) != 1 || clues[0].Kind != unit.ClueSpec || clues[0].Relation != unit.RelUsed ||
		!strings.Contains(clues[0].Text, "accumulates one request's phase events") {
		t.Fatalf("want the used type's spec as a used-relation spec clue, got %+v", clues)
	}
}

// When two symbols share a bare name, an import resolves the reference to the right
// one via fqn — the dependency's rule fires, the same-named local one doesn't.
func TestRelatedFinder_FqnDisambiguates(t *testing.T) {
	idx, err := Parse([]byte(`{
	  "framework/mw/trace.py::Middleware": { "fqn": "framework.mw.trace.Middleware", "cases": [], "rules": ["per-request only"] },
	  "app/local.py::Middleware": { "fqn": "app.local.Middleware", "cases": [], "rules": ["local rule — should NOT fire"] }
	}`))
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	write(t, filepath.Join(repo, "app", "handler.py"),
		"from framework.mw.trace import Middleware\n\ndef create():\n    return Middleware()\n")

	u := unit.UnitOf(unit.Fragment{
		Path:    "app/handler.py",
		Symbols: []string{"app/handler.py::create"},
		Diff:    "+    return Middleware()\n",
	})
	clues := NewRelatedFinder(Catalog{Local: idx}, repo, allGates).Find(u)
	if len(clues) != 1 || !strings.Contains(clues[0].Text, "per-request only") {
		t.Fatalf("want only the import-resolved (framework) rule, got %+v", clues)
	}
}

// Go: a `pkg.Symbol` selector resolves via the file's import to the right fqn.
func TestRelatedFinder_GoSelectorFqn(t *testing.T) {
	idx, err := Parse([]byte(`{
	  "framework/mw/trace.go::Middleware": { "fqn": "github.com/org/framework/mw/trace.Middleware", "cases": [], "rules": ["per-request only"] },
	  "app/local.go::Middleware": { "fqn": "github.com/org/app/local.Middleware", "cases": [], "rules": ["local rule — should NOT fire"] }
	}`))
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	write(t, filepath.Join(repo, "app", "handler.go"),
		"package app\n\nimport \"github.com/org/framework/mw/trace\"\n\nfunc create() {\n\t_ = trace.Middleware{}\n}\n")

	u := unit.UnitOf(unit.Fragment{
		Path:    "app/handler.go",
		Symbols: []string{"app/handler.go::create"},
		Diff:    "+\t_ = trace.Middleware{}\n",
	})
	clues := NewRelatedFinder(Catalog{Local: idx}, repo, allGates).Find(u)
	if len(clues) != 1 || !strings.Contains(clues[0].Text, "per-request only") {
		t.Fatalf("want only the import-resolved (framework) rule, got %+v", clues)
	}
}

// A Go used type whose source lives in this repo also yields its doc comment.
func TestRelatedFinder_GoUsedDocstring(t *testing.T) {
	idx, err := Parse([]byte(`{
	  "mw/trace.go::Middleware": { "fqn": "github.com/org/app/mw/trace.Middleware", "cases": [], "rules": ["per-request only"] }
	}`))
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	write(t, filepath.Join(repo, "mw", "trace.go"),
		"package trace\n\n// Middleware is per-request only — do not cache.\ntype Middleware struct{}\n")
	write(t, filepath.Join(repo, "app", "handler.go"),
		"package app\n\nimport \"github.com/org/app/mw/trace\"\n\nfunc create() {\n\t_ = trace.Middleware{}\n}\n")

	u := unit.UnitOf(unit.Fragment{
		Path:    "app/handler.go",
		Symbols: []string{"app/handler.go::create"},
		Diff:    "+\t_ = trace.Middleware{}\n",
	})
	clues := NewRelatedFinder(Catalog{Local: idx}, repo, allGates).Find(u)
	var rule, doc bool
	for _, c := range clues {
		if c.Relation != unit.RelUsed {
			continue
		}
		switch c.Kind {
		case unit.ClueRule:
			rule = strings.Contains(c.Text, "per-request only")
		case unit.ClueDoc:
			doc = strings.Contains(c.Text, "do not cache")
		}
	}
	if !rule || !doc {
		t.Fatalf("want used rule + used doc for the in-repo Go type, got %+v", clues)
	}
}

// A dependency entry lives in its own address space: reachable ONLY through an
// import-resolved fqn — never by bare name, and never as a local symbol's own
// spec (its relpath keys belong to the dependency's tree, not this repo's).
func TestRelatedFinder_DepEntryOnlyReachableByFqn(t *testing.T) {
	cat := Catalog{Deps: map[string]Entry{
		"framework.mw.trace.Middleware": {Fqn: "framework.mw.trace.Middleware", Rules: []string{"per-request only"}},
	}}
	repo := t.TempDir()

	// bare reference, no import → the dependency rule must NOT fire
	write(t, filepath.Join(repo, "app", "bare.py"), "def create():\n    return Middleware()\n")
	bare := unit.UnitOf(unit.Fragment{
		Path:    "app/bare.py",
		Symbols: []string{"app/bare.py::create"},
		Diff:    "+    return Middleware()\n",
	})
	if got := NewRelatedFinder(cat, repo, allGates).Find(bare); got != nil {
		t.Fatalf("dependency entry must not match by bare name, got %+v", got)
	}

	// import-resolved reference → the dependency rule fires via fqn
	write(t, filepath.Join(repo, "app", "handler.py"),
		"from framework.mw.trace import Middleware\n\ndef create():\n    return Middleware()\n")
	imported := unit.UnitOf(unit.Fragment{
		Path:    "app/handler.py",
		Symbols: []string{"app/handler.py::create"},
		Diff:    "+    return Middleware()\n",
	})
	clues := NewRelatedFinder(cat, repo, allGates).Find(imported)
	if len(clues) != 1 || clues[0].Kind != unit.ClueRule || clues[0].Relation != unit.RelUsed ||
		!strings.Contains(clues[0].Text, "per-request only") {
		t.Fatalf("want the dependency rule via fqn, got %+v", clues)
	}
}

// The dependency case: the used type has no spec entry at all — its docstring is
// read from the venv source (adoption-free).
func TestRelatedFinder_DepDocstring(t *testing.T) {
	repo := t.TempDir()
	write(t, filepath.Join(repo, "app", "handler.py"),
		"from framework.middleware.trace import PhaseEventMiddleware\n\ndef create():\n    return PhaseEventMiddleware()\n")
	write(t, filepath.Join(repo, ".venv", "lib", "python3.11", "site-packages", "framework", "middleware", "trace.py"),
		"class PhaseEventMiddleware:\n    \"\"\"Per-request only — do not cache/reuse.\"\"\"\n    pass\n")

	u := unit.UnitOf(unit.Fragment{
		Path:    "app/handler.py",
		Symbols: []string{"app/handler.py::create"},
		Diff:    "+    return PhaseEventMiddleware()\n",
	})
	clues := NewRelatedFinder(Catalog{}, repo, allGates).Find(u)
	if len(clues) != 1 || clues[0].Kind != unit.ClueDoc || clues[0].Relation != unit.RelUsed ||
		clues[0].Ref != "framework.middleware.trace.PhaseEventMiddleware" ||
		!strings.Contains(clues[0].Text, "Per-request only — do not cache/reuse.") {
		t.Fatalf("want one used-relation doc clue from the dependency docstring, got %+v", clues)
	}
}
