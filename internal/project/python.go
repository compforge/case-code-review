package project

import (
	"path"
	"strings"
)

var pythonProfile = profile{
	kind:    Python,
	markers: []string{"pyproject.toml", "setup.py"},
	classify: func(root, file string) FileRoles {
		switch strings.ToLower(path.Base(file)) {
		case "pyproject.toml", "setup.py":
			return FileRoles{RoleManifest}
		case "uv.lock":
			return FileRoles{RoleLock}
		}
		extension := strings.ToLower(path.Ext(file))
		switch extension {
		case ".py", ".pyi":
			roles := FileRoles{RoleSource}
			base := strings.ToLower(path.Base(file))
			if extension == ".py" && base == "__main__.py" {
				roles = append(roles, RoleEntrypoint)
			}
			return roles
		default:
			return FileRoles{RoleUnknown}
		}
	},
}

func enrichPythonRoles(file string, roles FileRoles, decorators, calls []string) FileRoles {
	if !roles.Has(RoleSource) || strings.ToLower(path.Ext(file)) != ".py" {
		return roles
	}
	for _, decorator := range decorators {
		parts := strings.Split(strings.ToLower(decorator), ".")
		if len(parts) < 2 {
			continue
		}
		switch parts[len(parts)-1] {
		case "get", "post", "put", "patch", "delete", "options", "head", "trace", "api_route", "websocket", "websocket_route":
			roles = roles.With(RoleHandler)
		}
	}
	if strings.EqualFold(path.Base(file), "main.py") {
		for _, call := range calls {
			if call == "FastAPI" {
				roles = roles.With(RoleEntrypoint)
				break
			}
		}
	}
	return roles
}
