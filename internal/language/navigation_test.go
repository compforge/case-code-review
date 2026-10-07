package language

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func navigationFixture(t testing.TB) (*Analyzer, Source) {
	t.Helper()
	repo := t.TempDir()
	source := Source{Path: "metrics.go", Content: "package telemetry\n\nfunc RecordToolCall(ok bool) {\n if !ok { println(\"error\") }\n}\n"}
	for path, content := range map[string]string{
		source.Path:       source.Content,
		"span.go":         "package telemetry\nfunc RecordToolResult(err error) {}\n",
		"metrics_test.go": "package telemetry\nfunc TestMetric() { RecordToolCall(false) }\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return NewAnalyzer(repo), source
}

func TestNavigationUsesPublishedDocument(t *testing.T) {
	a, source := navigationFixture(t)
	ctx := context.Background()
	local, err := a.Definitions(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if a.repository.Load() != nil {
		t.Fatal("navigation eagerly built repository")
	}
	index := a.Repository()
	graph, err := a.navigationGraph(ctx, source)
	if err != nil || graph != index.Graph {
		t.Fatalf("published graph not reused: %v", err)
	}
	shared, err := a.Definitions(ctx, source)
	if err != nil || !reflect.DeepEqual(shared, local) {
		t.Fatalf("definitions changed: local=%+v shared=%+v error=%v", local, shared, err)
	}
	// Test declarations are omitted from the repo map, but remain navigable.
	testSource := Source{Path: "metrics_test.go", Content: index.Sources["metrics_test.go"]}
	defs, err := a.Definitions(ctx, testSource)
	if err != nil || len(defs) != 1 || defs[0].Name != "TestMetric" {
		t.Fatalf("test definitions: %+v %v", defs, err)
	}
}

func TestNavigationFallsBackWithoutChangingPublication(t *testing.T) {
	a, source := navigationFixture(t)
	index := a.Repository()
	for _, tc := range []struct {
		name   string
		source Source
		want   string
	}{
		{"different content", Source{Path: source.Path, Content: "package telemetry\nfunc Changed() {}\n"}, "Changed"},
		{"unindexed file", Source{Path: "extra.go", Content: "package telemetry\nfunc Extra() {}\n"}, "Extra"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defs, err := a.Definitions(context.Background(), tc.source)
			if err != nil || len(defs) != 1 || defs[0].Name != tc.want {
				t.Fatalf("definitions: %+v %v", defs, err)
			}
			if a.Repository() != index || index.Sources[source.Path] != source.Content {
				t.Fatal("navigation mutated repository")
			}
		})
	}
	// Extraction can succeed for a file later omitted by the graph budget.
	omitted := Source{Path: "omitted.go", Content: "package telemetry\nfunc Omitted() {}\n"}
	index.Sources[omitted.Path] = omitted.Content
	defs, err := a.Definitions(context.Background(), omitted)
	if err != nil || len(defs) != 1 || defs[0].Name != "Omitted" {
		t.Fatalf("budget omission lost local fallback: %+v %v", defs, err)
	}
}

func TestNavigationDuringPublication(t *testing.T) {
	a, source := navigationFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				defs, err := a.Definitions(context.Background(), source)
				if err != nil || len(defs) != 1 || defs[0].Name != "RecordToolCall" {
					t.Errorf("definitions: %+v %v", defs, err)
				}
			}
		}()
	}
	a.Repository()
	wg.Wait()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Definitions(ctx, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query: %v", err)
	}
}

func BenchmarkNavigationDefinitions(b *testing.B) {
	for _, published := range []bool{false, true} {
		name := "local"
		if published {
			name = "published"
		}
		b.Run(name, func(b *testing.B) {
			a, source := navigationFixture(b)
			if published {
				a.Repository()
			}
			if _, err := a.Definitions(context.Background(), source); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := a.Definitions(context.Background(), source); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
