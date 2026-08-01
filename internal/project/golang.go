package project

import (
	"path"
	"strings"
)

var goProfile = profile{
	kind:    Go,
	markers: []string{"go.mod"},
	classify: func(file string) FileRole {
		switch strings.ToLower(path.Base(file)) {
		case "go.mod":
			return RoleManifest
		case "go.sum":
			return RoleLock
		}
		if strings.EqualFold(path.Ext(file), ".go") {
			return RoleSource
		}
		return RoleUnknown
	},
}
