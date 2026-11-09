package language

import cg "github.com/compforge/codegraph"

// Connection carries RepoKit's graph-backed grouping evidence into review recording.
//
// +why=`RepoKit owns Fragment grouping; CCR records its evidence without rebuilding dependency connections`
// +link=component://docs/unit-model.md
type Connection struct {
	Snapshot   string          `json:"snapshot"`
	Source     string          `json:"source"`
	Target     string          `json:"target"`
	SourcePath string          `json:"source_path,omitempty"`
	TargetPath string          `json:"target_path,omitempty"`
	Kind       cg.RelationKind `json:"kind"`
	Confidence cg.Confidence   `json:"confidence"`
	Touched    bool            `json:"touched"`
}
