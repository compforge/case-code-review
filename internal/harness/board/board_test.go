package board

import (
	"strings"
	"testing"
)

func interest(paths, symbols []string) Interest {
	in := Interest{Paths: map[string]bool{}, Symbols: map[string]bool{}}
	for _, p := range paths {
		in.Paths[p] = true
	}
	for _, s := range symbols {
		in.Symbols[s] = true
	}
	return in
}

func TestPullRoutesByInterest(t *testing.T) {
	b := New()
	b.Register("A", interest([]string{"a.go"}, []string{"a.go::F"}))
	b.Register("B", interest([]string{"b.go"}, nil))

	// A posts a fact about a.go::F; only a subscriber interested in it should see it.
	b.Publish(Bulletin{From: "A", Level: LevelConfirmed, Symbols: []string{"a.go::F"}, Text: "read a.go::F"})

	// A never sees its own post.
	if d, n := b.Pull("A"); n != 0 || d != "" {
		t.Fatalf("self-post leaked to poster: %q", d)
	}
	// B isn't interested in a.go::F.
	if _, n := b.Pull("B"); n != 0 {
		t.Fatal("B pulled an unrelated bulletin")
	}

	// C is interested in a.go::F → sees it, with the isolation stamp.
	b.Register("C", interest(nil, []string{"a.go::F"}))
	d, n := b.Pull("C")
	if n != 1 || !strings.Contains(d, "read a.go::F") {
		t.Fatalf("C should receive the routed bulletin: n=%d d=%q", n, d)
	}
	if !strings.Contains(d, "observation") || !strings.Contains(d, "do NOT report it as your own finding") {
		t.Fatalf("digest missing the isolation stamp: %q", d)
	}
}

func TestPullIsIncremental(t *testing.T) {
	b := New()
	b.Register("C", interest([]string{"x.go"}, nil))
	b.Publish(Bulletin{From: "A", Level: LevelConfirmed, Paths: []string{"x.go"}, Text: "one"})

	if _, n := b.Pull("C"); n != 1 {
		t.Fatalf("first pull should see the one bulletin, got %d", n)
	}
	// Nothing new → empty (zero token).
	if d, n := b.Pull("C"); n != 0 || d != "" {
		t.Fatalf("second pull with no new posts must be empty: %q", d)
	}
	// A new post is seen on the next pull only.
	b.Publish(Bulletin{From: "A", Level: LevelConfirmed, Paths: []string{"x.go"}, Text: "two"})
	if d, n := b.Pull("C"); n != 1 || !strings.Contains(d, "two") || strings.Contains(d, "one") {
		t.Fatalf("incremental pull must return only the new bulletin: %q", d)
	}
}

func TestScoreSymbolOutranksPathAndLevel(t *testing.T) {
	in := interest([]string{"p.go"}, []string{"p.go::F"})
	sym := Bulletin{Level: LevelObservation, Symbols: []string{"p.go::F"}}
	pth := Bulletin{Level: LevelConfirmed, Paths: []string{"p.go"}}
	if in.score(sym) <= in.score(pth) {
		t.Fatalf("symbol observation (%d) should outrank path confirmed (%d)", in.score(sym), in.score(pth))
	}
	if in.score(Bulletin{Paths: []string{"other.go"}}) != 0 {
		t.Fatal("unrelated bulletin must score 0")
	}
}

func TestPullCapsAndCountsOverflow(t *testing.T) {
	b := New()
	b.Register("C", interest([]string{"x.go"}, nil))
	for range maxPerPull + 3 {
		b.Publish(Bulletin{From: "A", Level: LevelConfirmed, Paths: []string{"x.go"}, Text: "note"})
	}
	d, n := b.Pull("C")
	if n != maxPerPull {
		t.Fatalf("pull must cap at %d, got %d", maxPerPull, n)
	}
	if !strings.Contains(d, "3 more relevant notes") {
		t.Fatalf("overflow must be summarized, not dropped silently: %q", d)
	}
}

func TestPostedDrains(t *testing.T) {
	b := New()
	b.Publish(Bulletin{From: "A", Text: "one"})
	b.Publish(Bulletin{From: "B", Text: "two"})
	if got := b.Posted(); len(got) != 2 {
		t.Fatalf("Posted should return all publishes, got %d", len(got))
	}
	if got := b.Posted(); len(got) != 0 {
		t.Fatalf("Posted must drain, second call got %d", len(got))
	}
}
