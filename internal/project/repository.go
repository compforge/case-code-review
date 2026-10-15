// Package project projects repository facts into review context.
package project

import (
	"path"
	"slices"

	cg "github.com/compforge/codegraph"
	"github.com/compforge/repocli/toolkit/go"
)

// Repository consumes repocli's engineering boundaries from the reviewed snapshot.
// Language-specific roles are review projections, never a second Component discovery.
type Repository struct{ layout repocli.Layout }

func NewRepository(layout repocli.Layout) *Repository { return &Repository{layout: layout} }

var materialTags, _ = cg.NewTagMatcher(nil)

// Resolve keeps engineering ownership independent of each file's language.
// +spec=`Component roots come only from the captured repocli layout; nested manifests and mixed languages do not create CCR-only boundaries`
// +link=component://docs/project.md
func (r *Repository) Resolve(file string) (Component, FileRoles, bool) {
	file = path.Clean(file)
	owner := r.layout.Owner(file)
	if owner == nil {
		return Component{}, nil, false
	}
	kind := Kind(owner.Language)
	component := Component{Root: owner.Root, Kind: kind}
	if slices.Contains(materialTags.Match(file), cg.LockfileTag) {
		return component, FileRoles{RoleLock}, true
	}
	for _, classify := range roleClassifiers {
		if roles := classify(owner.Root, file); roles.Known() {
			return component, roles, true
		}
	}
	return component, FileRoles{RoleUnknown}, true
}
