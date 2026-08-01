// Package project models repository-level knowledge used to form and brief
// review units. Repository and Component are static project boundaries; a Unit
// remains the dynamic boundary of one review.
package project

import "path"

// Repository is the reviewed Git snapshot and the entry point for resolving
// its manifest-defined Components. Root identifies the local repository while
// exists must answer against the reviewed tree (workspace, range target, or
// commit), which may differ from the current checkout.
type Repository struct {
	Root   string
	exists func(string) bool
	cache  map[string]bool
}

func NewRepository(root string, exists func(string) bool) *Repository {
	return &Repository{Root: root, exists: exists, cache: make(map[string]bool)}
}

// Resolve returns the nearest owning Component and the file's roles. Files may
// legitimately belong to no Component (for example a repository-level README).
func (r *Repository) Resolve(file string) (Component, FileRoles, bool) {
	file = path.Clean(file)
	dir := path.Dir(file)
	for {
		var matched []profile
		for _, candidate := range profiles {
			if r.hasMarker(dir, candidate.markers) {
				matched = append(matched, candidate)
			}
		}

		// A polyglot directory may carry several manifests. Prefer the profile
		// that understands this file; an unrelated config remains unowned rather
		// than being assigned to an arbitrary ecosystem.
		for _, candidate := range matched {
			if roles := candidate.classify(dir, file); roles.Known() {
				return Component{Root: dir, Kind: candidate.kind}, roles, true
			}
		}
		if len(matched) == 1 {
			return Component{Root: dir, Kind: matched[0].kind}, FileRoles{RoleUnknown}, true
		}
		if len(matched) > 1 {
			return Component{}, nil, false
		}

		if dir == "." {
			break
		}
		dir = path.Dir(dir)
	}
	return Component{}, nil, false
}

func (r *Repository) hasMarker(root string, markers []string) bool {
	for _, marker := range markers {
		candidate := marker
		if root != "." {
			candidate = path.Join(root, marker)
		}
		if found, ok := r.cache[candidate]; ok {
			if found {
				return true
			}
			continue
		}
		found := r.exists != nil && r.exists(candidate)
		r.cache[candidate] = found
		if found {
			return true
		}
	}
	return false
}
