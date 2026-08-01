package project

// FileRole is what a file is inside its Component. It deliberately does not
// encode evidentiary strength: the same manifest may be decisive for one
// hypothesis and merely background for another.
type FileRole string

const (
	RoleUnknown  FileRole = "unknown"
	RoleSource   FileRole = "source"
	RoleManifest FileRole = "manifest"
	RoleLock     FileRole = "lock"
)
