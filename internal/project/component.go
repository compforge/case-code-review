package project

// Kind identifies the project ecosystem that owns a Component.
type Kind string

const (
	Python Kind = "python"
	Go     Kind = "go"
)

// Component is one manifest-defined project boundary inside a Repository.
// Root is a slash-separated repository-relative directory; "." is the repo root.
type Component struct {
	Root string
	Kind Kind
}

type profile struct {
	kind     Kind
	markers  []string
	classify func(root, file string) FileRoles
}

var profiles = []profile{pythonProfile, goProfile}

// EnrichFileRoles projects source-language facts into Component semantics.
// Language owns extraction; Project owns what those facts mean to review.
func EnrichFileRoles(component Component, file string, roles FileRoles, decorators, calls []string) FileRoles {
	if component.Kind == Python {
		return enrichPythonRoles(file, roles, decorators, calls)
	}
	return roles
}
