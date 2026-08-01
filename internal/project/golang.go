package project

import (
	"path"
	"strings"
)

var goProfile = profile{
	kind:    Go,
	markers: []string{"go.mod"},
	classify: func(_, file string) FileRoles {
		switch strings.ToLower(path.Base(file)) {
		case "go.mod":
			return FileRoles{RoleManifest}
		case "go.sum":
			return FileRoles{RoleLock}
		}
		if strings.EqualFold(path.Ext(file), ".go") {
			roles := FileRoles{RoleSource}
			// Go does not require this filename, but main.go is the dominant
			// repository convention for an executable package boundary.
			if strings.EqualFold(path.Base(file), "main.go") {
				roles = append(roles, RoleEntrypoint)
			}
			return roles
		}
		return FileRoles{RoleUnknown}
	},
}
