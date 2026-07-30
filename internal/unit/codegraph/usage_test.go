package codegraph

import (
	"testing"
)

func TestFindUsages(t *testing.T) {
	repo := newRepo(t, map[string]string{
		"svc.go":  "package p\n\nfunc Get() int { return 1 }\n",
		"user.go": "package p\n\nfunc UseIt() int {\n\treturn Get()\n}\n",
		"more.go": "package p\n\nvar v = Get()\n",
	})

	// The unit's own file is excluded (its internal uses are already visible in
	// the inlined source); hits elsewhere come back with line text for rendering.
	got := FindUsages(repo, nil, []string{"svc.go::Get"}, map[string]bool{"svc.go": true})
	files := map[string]string{}
	for _, u := range got {
		if u.Symbol != "svc.go::Get" || u.Line <= 0 || u.Text == "" {
			t.Fatalf("malformed usage: %+v", u)
		}
		files[u.File] = u.Text
	}
	if files["svc.go"] != "" {
		t.Fatalf("own file must be excluded, got %+v", got)
	}
	if files["user.go"] != "return Get()" || files["more.go"] != "var v = Get()" {
		t.Fatalf("missing usage sites: %+v", got)
	}

	// No repo → degrade to nil.
	if got := FindUsages("", nil, []string{"svc.go::Get"}, nil); got != nil {
		t.Fatalf("want nil without a repo, got %+v", got)
	}
}

func TestFindUsages_SkipsCommentProse(t *testing.T) {
	repo := newRepo(t, map[string]string{
		"g.go": "package p\n\nfunc graph() int { return 1 }\n",
		// A dereference assignment starts with '*' but is NOT comment prose.
		"use.go": "package p\n\n// the call graph is walked lazily\nvar n = graph()\n\nfunc set(p *int) {\n\t*p = graph()\n}\n",
	})
	got := FindUsages(repo, nil, []string{"g.go::graph"}, map[string]bool{"g.go": true})
	texts := map[string]bool{}
	for _, u := range got {
		texts[u.Text] = true
	}
	if !texts["var n = graph()"] || !texts["*p = graph()"] || len(got) != 2 {
		t.Fatalf("want the var and deref usages only, got %+v", got)
	}
}
