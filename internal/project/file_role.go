package project

import "strings"

// FileRole is one stable responsibility a file has inside its Component. A
// source file may carry additional semantic roles such as entrypoint or
// handler; roles describe project structure, not evidentiary strength.
type FileRole string

const (
	RoleUnknown    FileRole = "unknown"
	RoleSource     FileRole = "source"
	RoleManifest   FileRole = "manifest"
	RoleLock       FileRole = "lock"
	RoleEntrypoint FileRole = "entrypoint"
	RoleHandler    FileRole = "handler"
)

// FileRoles is the composable role set for one file. Source remains present
// when a file is also an entrypoint or handler, so admission and semantic
// importance do not compete for one enum slot.
type FileRoles []FileRole

func (r FileRoles) Has(role FileRole) bool {
	for _, candidate := range r {
		if candidate == role {
			return true
		}
	}
	return false
}

func (r FileRoles) With(role FileRole) FileRoles {
	if r.Has(role) {
		return r
	}
	return append(r, role)
}

func (r FileRoles) Known() bool {
	return len(r) > 0 && !r.Has(RoleUnknown)
}

func (r FileRoles) String() string {
	values := make([]string, 0, len(r))
	for _, role := range r {
		values = append(values, string(role))
	}
	return strings.Join(values, ",")
}
