package project

import (
	"path"
	"strings"
)

var typeScriptProfile = profile{
	kind:    TypeScript,
	markers: []string{"package.json"},
	classify: func(root, file string) FileRoles {
		base := strings.ToLower(path.Base(file))
		switch base {
		case "package.json":
			return FileRoles{RoleManifest}
		case "bun.lock", "bun.lockb", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock":
			return FileRoles{RoleLock}
		case "version":
			if path.Dir(file) == root {
				return FileRoles{RoleVersion}
			}
		}
		if (base == "tsconfig.json" || strings.HasPrefix(base, "tsconfig.")) && strings.HasSuffix(base, ".json") {
			// Compiler configuration affects every source file in the package,
			// so Unit Review consumes it as Component context rather than a target.
			return FileRoles{RoleManifest}
		}

		extension := strings.ToLower(path.Ext(file))
		switch extension {
		case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
			roles := FileRoles{RoleSource}
			stem := strings.TrimSuffix(base, extension)
			if strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec") {
				roles = append(roles, RoleTest)
			}
			if strings.EqualFold(stem, "main") {
				roles = append(roles, RoleEntrypoint)
			}
			return roles
		default:
			return FileRoles{RoleUnknown}
		}
	},
}
