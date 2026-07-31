package finding

// Finding is a code issue approved for public delivery. Diff review reaches it
// only through Trial; full-file scan produces it directly.
type Finding struct {
	// HypothesisID joins a diff-review result to its pre-Trial claim and
	// Assessment in the session transcript. Full-file scan leaves it empty.
	HypothesisID   string `json:"hypothesis_id,omitempty"`
	Path           string `json:"path"`
	Content        string `json:"content"`
	SuggestionCode string `json:"suggestion_code,omitempty"`
	ExistingCode   string `json:"existing_code,omitempty"`
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
	// SymbolID is the enclosing function's symbol-id (<relpath>::<symbol>) the
	// comment lands in, resolved from the post-change file. Lets callers key review
	// history by symbol rather than by drift-prone line numbers. Empty when the line
	// resolves to no function (or an unsupported file).
	SymbolID string `json:"symbol_id,omitempty"`
	// Fingerprint is the finding's stable identity: a short hash of path+content,
	// deliberately excluding line numbers (relocation and later edits shift them).
	// Human labels (important/minor/…) and posterior evidence key on it, so a
	// re-run reproducing the same finding joins to the same label.
	Fingerprint string `json:"fingerprint,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	// Alias is the routing alias of the model that produced this comment (from
	// routing.models[].alias). Lets callers compare per-model output when a multi-model
	// pool spreads files across models (e.g. round-robin). Empty for single-model runs
	// or when no alias is configured.
	Alias string `json:"alias,omitempty"`
	// Category is the engine-declared finding class, one of: bug, security,
	// performance, maintainability, test, style, documentation, other. Structured so
	// consumers group/filter/gate without re-parsing content; values outside the
	// vocabulary are dropped at parse time (a free-form word would fragment grouping).
	Category string `json:"category,omitempty"`
	// Severity is the engine-declared importance, one of: critical, high, medium, low.
	// Deliberately self-reported: its DISAGREEMENT with human labels
	// (important/minor/… — eval/README §8.5) is itself a calibration signal.
	Severity string `json:"severity,omitempty"`
}
