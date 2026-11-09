package language

import "testing"

// +case:id=semantic_usage_counts,desc=`A function is called and passed as a value; a variable is read and updated`,expect=`Usage counts retain each semantic occurrence once without admitting declaration dependency edges`
// +link=component://docs/language.md
func TestUsageCountsSemanticOccurrences(t *testing.T) {
	a := referenceRepo(t, map[string]string{
		"app.go": "package app\nvar count int\nfunc handler(){}\nfunc register(f func()){}\nfunc entry(){handler(); register(handler); count += 1; _ = count}\n",
	})
	index := a.Repository()
	for symbol, want := range map[string]int{"app.go::handler": 2, "app.go::count": 3} {
		uses := index.UsesOf(symbol)
		if len(uses) != want {
			t.Fatalf("%s uses=%d want=%d: %+v", symbol, len(uses), want, uses)
		}
		seen := map[string]bool{}
		for _, node := range uses {
			if !node.Kind.IsOccurrence() || ReviewSymbolID(node) != "" {
				t.Fatalf("usage acquired declaration identity: %+v", node)
			}
			if seen[node.ID] {
				t.Fatalf("duplicate usage occurrence: %+v", node)
			}
			seen[node.ID] = true
		}
	}
}
