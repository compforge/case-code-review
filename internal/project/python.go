package project

import (
	"path"
	"strings"
)

var pythonProfile = profile{
	kind:    Python,
	markers: []string{"pyproject.toml", "setup.py"},
	classify: func(file string) FileRole {
		switch strings.ToLower(path.Base(file)) {
		case "pyproject.toml", "setup.py":
			return RoleManifest
		case "uv.lock":
			return RoleLock
		}
		switch strings.ToLower(path.Ext(file)) {
		case ".py", ".pyi":
			return RoleSource
		default:
			return RoleUnknown
		}
	},
}
