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
	classify func(string) FileRole
}

var profiles = []profile{pythonProfile, goProfile}
