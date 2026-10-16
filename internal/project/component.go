package project

// Kind describes the primary language metadata of an engineering Component.
type Kind string

const (
	Python     Kind = "python"
	Go         Kind = "go"
	TypeScript Kind = "typescript"
	JavaScript Kind = "javascript"
)

// Component projects one repocli engineering boundary inside a Repository.
// Root is a slash-separated repository-relative directory; "." is the repo root.
type Component struct {
	Root string
	Kind Kind
}
