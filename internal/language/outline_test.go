package language

import (
	"context"
	"strings"
	"testing"
)

func TestFileOutlineUsesLanguageNativeDataMembers(t *testing.T) {
	tests := []struct {
		path, source string
		want         []string
		reject       []string
	}{
		{
			path: "service.go",
			source: `package service

type Service struct {
	repo Repository
	timeout time.Duration
}

func (s *Service) Run() error { return nil }
`,
			want: []string{"- type type Service struct", "- field repo Repository", "- field timeout time.Duration", "- method func (s *Service) Run() error"},
		},
		{
			path: "Service.java",
			source: `class Service {
  private final String token = "do-not-keep";
  int retries;
  void run() {}
}
`,
			want:   []string{"- class class Service", "- field private final String token", "- field int retries", "- method void run()"},
			reject: []string{"do-not-keep"},
		},
		{
			path: "service.ts",
			source: `class Service {
  private token: string = "do-not-keep";
  readonly retries: number;
  run(): void {}
}
interface Request { id: string; }
`,
			want:   []string{"- class class Service", "- property private token: string", "- property readonly retries: number", "- method run(): void", "- interface interface Request", "- property id: string"},
			reject: []string{"do-not-keep"},
		},
		{
			path: "service.py",
			source: `class Service:
    token: str = "do-not-keep"
    retries = 3

    def run(self):
        local = "not-an-attribute"
`,
			want:   []string{"- class class Service:", "- attribute token: str", "- attribute retries", "- method def run(self):"},
			reject: []string{"do-not-keep", "not-an-attribute", "local"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			outline, err := NewAnalyzer("").FileOutline(context.Background(), Source{Path: tt.path, Content: tt.source})
			if err != nil {
				t.Fatal(err)
			}
			got := outline.Render()
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("outline missing %q:\n%s", want, got)
				}
			}
			for _, reject := range tt.reject {
				if strings.Contains(got, reject) {
					t.Errorf("outline retained %q:\n%s", reject, got)
				}
			}
		})
	}
}

func TestFileOutlineRangeKeepsOwnerAndIntersectingMembers(t *testing.T) {
	source := Source{Path: "service.go", Content: `package service

type Service struct { repo Repository }

func (s *Service) Run() {}
func helper() {}
`}
	outline, err := NewAnalyzer("").FileOutline(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	got := outline.RenderRange(5, 5)
	if !strings.Contains(got, "type Service") || !strings.Contains(got, "Service) Run") || strings.Contains(got, "helper") {
		t.Fatalf("ranged outline off:\n%s", got)
	}
}

func TestJSONFileOutlineKeepsKeysAndCompactsLongStrings(t *testing.T) {
	outline, err := NewAnalyzer("").FileOutline(context.Background(), Source{
		Path:    "config.json",
		Content: `{"service":{"name":"long-service-name","enabled":true,"replicas":3},"tag":"x"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := outline.Render()
	for _, want := range []string{`"service"`, `"name": "…"`, `"enabled": "…"`, `"replicas": 3`, `"tag": "x"`} {
		if !strings.Contains(got, want) {
			t.Errorf("JSON outline missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "long-service-name") {
		t.Fatalf("JSON outline retained long value:\n%s", got)
	}
}

func TestMarkdownFileOutlineKeepsHeadingsOnly(t *testing.T) {
	outline, err := NewAnalyzer("").FileOutline(context.Background(), Source{
		Path:    "design.md",
		Content: "preamble secret\n# Design\nlong section secret\n## Flow\nmore details\n```md\n# not a heading\n```\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := outline.Render()
	for _, want := range []string{"# Design", "## Flow", "…"} {
		if !strings.Contains(got, want) {
			t.Errorf("Markdown outline missing %q:\n%s", want, got)
		}
	}
	for _, reject := range []string{"preamble secret", "long section secret", "more details", "not a heading"} {
		if strings.Contains(got, reject) {
			t.Errorf("Markdown outline retained %q:\n%s", reject, got)
		}
	}
}
