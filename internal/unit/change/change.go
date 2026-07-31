// Package change models the source material from which review Units are formed.
package change

// Change is one file's review input. Diff review fills the unified diff and
// file content; full-file scan fills the content and leaves Diff empty.
type Change struct {
	OldPath        string `json:"old_path"`
	NewPath        string `json:"new_path"`
	Diff           string `json:"diff"`
	NewFileContent string `json:"new_file_content"`
	IsBinary       bool   `json:"is_binary"`
	IsDeleted      bool   `json:"is_deleted"`
	IsNew          bool   `json:"is_new"`
	IsRenamed      bool   `json:"is_renamed"`
	Insertions     int64  `json:"insertions"`
	Deletions      int64  `json:"deletions"`
}
