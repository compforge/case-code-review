package main

import "testing"

func TestResolveVersion(t *testing.T) {
	tests := []struct {
		name    string
		stamped string
		module  string
		want    string
	}{
		{name: "ldflags wins", stamped: "v1.10.0", module: "v1.9.1", want: "v1.10.0"},
		{name: "go install module version", stamped: "dev", module: "v1.10.0", want: "v1.10.0"},
		{name: "local build", stamped: "dev", module: "(devel)", want: "dev"},
		{name: "missing build info", stamped: "dev", want: "dev"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVersion(tt.stamped, tt.module); got != tt.want {
				t.Fatalf("resolveVersion(%q, %q) = %q, want %q", tt.stamped, tt.module, got, tt.want)
			}
		})
	}
}
