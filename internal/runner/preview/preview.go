package preview

// ExcludeReason describes why a file was excluded from review. Shared by
// both diff review (internal/runner) and full-file scan (internal/runner/scan).
type ExcludeReason string

const (
	ExcludeNone          ExcludeReason = ""
	ExcludeUserRule      ExcludeReason = "user_exclude"
	ExcludeExtension     ExcludeReason = "unsupported_ext"
	ExcludeDefaultPath   ExcludeReason = "default_path"
	ExcludeDeleted       ExcludeReason = "deleted"
	ExcludeBinary        ExcludeReason = "binary"
	ExcludeNonReviewRole ExcludeReason = "non_review_role"
)

// Entry is one file's preview record (mode-agnostic).
type Entry struct {
	Path            string        `json:"path"`
	Status          string        `json:"status"`
	Insertions      int64         `json:"insertions"`
	Deletions       int64         `json:"deletions"`
	WillReview      bool          `json:"will_review"`
	ProvidesContext bool          `json:"provides_context,omitempty"`
	ComponentRoot   string        `json:"component_root,omitempty"`
	ComponentKind   string        `json:"component_kind,omitempty"`
	FileRole        string        `json:"file_role,omitempty"`
	ExcludeReason   ExcludeReason `json:"exclude_reason,omitempty"`
}

// Preview is the full preview result, mode-agnostic so cmd/ccr
// can render it the same way for review and scan.
type Preview struct {
	Entries         []Entry `json:"files"`
	TotalInsertions int64   `json:"total_insertions"`
	TotalDeletions  int64   `json:"total_deletions"`
	TotalFiles      int     `json:"total_files"`
	ReviewableCount int     `json:"reviewable_count"`
	ContextCount    int     `json:"context_count,omitempty"`
	ExcludedCount   int     `json:"excluded_count"`
}
