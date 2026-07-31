package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

const repositoryURL = "https://github.com/compforge/case-code-review"

// Set via ldflags: -X main.Version=x.y.z -X main.GitCommit=abc123 -X main.BuildDate=2026-01-01T00:00:00Z
var (
	Version   = "dev"
	GitCommit = ""
	BuildDate = ""
)

func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	Version = resolveVersion(Version, info.Main.Version)
}

// resolveVersion preserves release builds stamped with -ldflags while making
// `go install ...@version` report the module version embedded by the Go tool.
func resolveVersion(stamped, module string) string {
	if stamped != "dev" || module == "" || module == "(devel)" {
		return stamped
	}
	return module
}

// versionString is the build identity stamped into session manifests
// ("v1.7.1 (abc123)" / "dev").
func versionString() string {
	if GitCommit != "" {
		return Version + " (" + GitCommit + ")"
	}
	return Version
}

func printVersion() {
	fmt.Printf("case-code-review %s", Version)
	if GitCommit != "" {
		fmt.Printf(" (%s)", GitCommit)
	}
	fmt.Printf(" %s/%s\n", runtime.GOOS, runtime.GOARCH)
	if BuildDate != "" {
		fmt.Printf("built at: %s\n", BuildDate)
	}
	fmt.Println(repositoryURL)
}
