// Package project projects repository facts into review context.
package project

import (
	"path"

	"github.com/compforge/repocli/toolkit/go"
)

// Repository consumes repocli's engineering boundaries from the reviewed snapshot.
// Language-specific roles are review projections, never a second Component discovery.
type Repository struct{ layout repocli.Layout }

func NewRepository(layout repocli.Layout) *Repository { return &Repository{layout: layout} }

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
	if path.Dir(file) == owner.Root && path.Base(file) == "VERSION" && (kind == Go || kind == TypeScript || kind == JavaScript) {
		return component, FileRoles{RoleVersion}, true
	}
	var roles FileRoles
	for _, role := range repocli.SourceRoles(file, nil) {
		roles = roles.With(FileRole(role))
	}
	if len(roles) == 0 {
		roles = FileRoles{RoleUnknown}
	}
	return component, roles, true
}
