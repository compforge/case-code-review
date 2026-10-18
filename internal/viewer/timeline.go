package viewer

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/compforge/go-stdx/timeline"
	"github.com/qiankunli/case-code-review/internal/recording"
)

type timelineRow struct {
	ScopeID     string
	SessionID   string
	ExecutionID string
	timeline.Stage
	OffsetMS      int64
	DurationMS    int64
	MissingParent bool
	Depth         int
	HasChildren   bool
	StartPercent  float64
	WidthPercent  float64
}

func (row timelineRow) Label() string {
	if row.Name == "model.attempt" || row.Name == "llm.request" {
		return "Model call"
	}
	if row.Name == "turn" {
		if turn, ok := timeline.AttributeValue[int](row.Attributes, "turn"); ok {
			return fmt.Sprintf("turn %d", turn)
		}
	}
	if row.Name == "review.pipeline" {
		if unit, ok := timeline.AttributeValue[string](row.Attributes, "unit_id"); ok && unit != "" {
			return "Pipeline · " + unit
		}
	}
	return row.Name
}

// Layout preserves hierarchy and overlapping intervals; it never sums durations.
func timelineRows(doc *timeline.Snapshot) []timelineRow {
	if doc == nil {
		return nil
	}
	stages := append([]timeline.Stage(nil), doc.Stages...)
	sort.SliceStable(stages, func(i, j int) bool {
		if stages[i].StartedAt.Equal(stages[j].StartedAt) {
			return stages[i].ID < stages[j].ID
		}
		return stages[i].StartedAt.Before(stages[j].StartedAt)
	})
	index := map[timeline.StageID]timeline.Stage{}
	for _, stage := range stages {
		index[stage.ID] = stage
	}
	inheritedAttribute := func(stage timeline.Stage, key string) string {
		seen := map[timeline.StageID]bool{}
		for stage.ID != "" && !seen[stage.ID] {
			seen[stage.ID] = true
			if id, ok := timeline.AttributeValue[string](stage.Attributes, key); ok && id != "" {
				return id
			}
			stage = index[stage.ParentID]
		}
		return ""
	}
	known := map[timeline.StageID]bool{doc.RootStageID: true}
	children := map[timeline.StageID][]timeline.Stage{}
	for _, stage := range stages {
		known[stage.ID] = true
		children[stage.ParentID] = append(children[stage.ParentID], stage)
	}
	seen := map[timeline.StageID]bool{}
	var rows []timelineRow
	var visit func(timeline.Stage, int)
	visit = func(stage timeline.Stage, depth int) {
		if seen[stage.ID] {
			return
		}
		seen[stage.ID] = true
		rows = append(rows, timelineRow{Stage: stage, ScopeID: inheritedAttribute(stage, "scope_id"), SessionID: inheritedAttribute(stage, "session_id"), ExecutionID: inheritedAttribute(stage, "execution_id"), OffsetMS: stage.StartedAt.Sub(doc.StartedAt).Milliseconds(), DurationMS: stage.Duration(doc.FinishedAt).Milliseconds(), MissingParent: stage.ParentID != "" && !known[stage.ParentID], Depth: depth, HasChildren: len(children[stage.ID]) > 0})
		for _, child := range children[stage.ID] {
			visit(child, depth+1)
		}
	}
	for _, stage := range stages {
		if stage.ParentID == doc.RootStageID || !known[stage.ParentID] {
			visit(stage, 0)
		}
	}
	for _, stage := range stages {
		if !seen[stage.ID] {
			visit(stage, 0)
		}
	}
	return rows
}

type timelineView struct {
	Rows       []timelineRow
	WindowMS   int64
	Incomplete int
	Lanes      []timelineLane
	Children   map[timeline.StageID][]timelineRow
}

// spec: Bars share source timestamps; unknown end times are markers, never
// intervals extended to the current wall clock. Elapsed is kept for the label.
func layoutTimeline(doc *timeline.Snapshot) timelineView {
	view := timelineView{Rows: timelineRows(doc)}
	if doc == nil {
		return view
	}
	end := doc.FinishedAt
	for _, row := range view.Rows {
		if row.StartedAt.After(end) {
			end = row.StartedAt
		}
		if row.FinishedAt.After(end) {
			end = row.FinishedAt
		}
	}
	window := end.Sub(doc.StartedAt)
	if window <= 0 {
		window = time.Millisecond
	}
	view.WindowMS = window.Milliseconds()
	for i := range view.Rows {
		row := &view.Rows[i]
		row.StartPercent = max(0, min(100, 100*float64(row.StartedAt.Sub(doc.StartedAt))/float64(window)))
		if row.FinishedAt.IsZero() {
			view.Incomplete++
			continue
		}
		row.WidthPercent = max(0, min(100-row.StartPercent, 100*float64(row.FinishedAt.Sub(row.StartedAt))/float64(window)))
	}
	view.Lanes = stageLanes(view.Rows)
	view.Children = map[timeline.StageID][]timelineRow{}
	for _, row := range view.Rows {
		view.Children[row.ParentID] = append(view.Children[row.ParentID], row)
	}
	return view
}

// A lane groups related work, while each bar retains its native Stage identity.
// Nested work is expandable inside the lane; overlapping roots get separate tracks.
type timelineLane struct {
	Phase    string
	Label    string
	Bars     []timelineRow
	Children []timelineRow
	Tracks   [][]timelineRow
	Tree     []*timelineBranch
}

type timelineBranch struct {
	timelineRow
	Children []*timelineBranch
}

// Only recorded ParentIDs define nesting. Lane roots may have parents displayed
// in another lane; they remain roots here without inventing a new relationship.
func stageTree(rows []timelineRow) []*timelineBranch {
	index := map[timeline.StageID]timelineRow{}
	children := map[timeline.StageID][]timelineRow{}
	for _, row := range rows {
		index[row.ID] = row
		children[row.ParentID] = append(children[row.ParentID], row)
	}
	seen := map[timeline.StageID]bool{}
	var visit func(timelineRow, int) *timelineBranch
	visit = func(row timelineRow, depth int) *timelineBranch {
		if seen[row.ID] {
			return nil
		}
		seen[row.ID] = true
		row.Depth = depth
		node := &timelineBranch{timelineRow: row}
		for _, child := range children[row.ID] {
			if nested := visit(child, depth+1); nested != nil {
				node.Children = append(node.Children, nested)
			}
		}
		return node
	}
	var roots []*timelineBranch
	for _, row := range rows {
		if _, ok := index[row.ParentID]; !ok {
			if node := visit(row, 0); node != nil {
				roots = append(roots, node)
			}
		}
	}
	for _, row := range rows {
		if node := visit(row, 0); node != nil {
			roots = append(roots, node)
		}
	}
	return roots
}

// +spec=`All stages remain reachable in swimlanes on the shared review clock; R1/R2 loops stay separate, overlapping roots use separate tracks, and missing ends are never extended`
func stageLanes(rows []timelineRow) []timelineLane {
	byID := map[timeline.StageID]timelineRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	type owner struct{ key, phase, label string }
	owners := map[timeline.StageID]owner{}
	for _, row := range rows {
		group := owner{key: "Review", phase: "Review", label: "Other stages"}
		seen := map[timeline.StageID]bool{}
		for ancestor := row; ancestor.ID != "" && !seen[ancestor.ID]; ancestor = byID[ancestor.ParentID] {
			seen[ancestor.ID] = true
			if ancestor.Name == "execution" {
				task, _ := timeline.AttributeValue[string](ancestor.Attributes, "task_type")
				phase := ""
				switch recording.TaskType(task) {
				case recording.MainTask:
					phase = "R1"
				case recording.HypothesisReviewTask:
					phase = "R2"
				}
				if phase != "" {
					key := ancestor.SessionID
					if key == "" {
						key = string(ancestor.ID)
					}
					label := ancestor.ScopeID
					if label == "" {
						label = string(ancestor.ID)
					}
					group = owner{key: phase + ":" + key, phase: phase, label: label}
					break
				}
			}
			if ancestor.Name == "review.pipeline" {
				group = owner{key: "Unit", phase: "Unit", label: "Pipelines (R1 → R2 → Trial)"}
				break
			}
			if strings.HasPrefix(ancestor.Name, "trial.") {
				group = owner{key: "R3", phase: "R3", label: "Trial"}
				break
			}
			if strings.HasPrefix(ancestor.Name, "diff.") || strings.HasPrefix(ancestor.Name, "project.") || strings.HasPrefix(ancestor.Name, "codegraph.") || strings.HasPrefix(ancestor.Name, "unit.formation") {
				group = owner{key: "Prepare", phase: "Prepare", label: "Repository & materials"}
			}
		}
		owners[row.ID] = group
	}
	var lanes []timelineLane
	index := map[string]int{}
	for _, row := range rows {
		group := owners[row.ID]
		i, ok := index[group.key]
		if !ok {
			i = len(lanes)
			index[group.key] = i
			lanes = append(lanes, timelineLane{Phase: group.phase, Label: group.label})
		}
		if parent, ok := owners[row.ParentID]; !ok || parent.key != group.key || row.Name == "execution" {
			lanes[i].Bars = append(lanes[i].Bars, row)
		} else {
			lanes[i].Children = append(lanes[i].Children, row)
		}
	}
	order := map[string]int{"Prepare": 0, "R1": 1, "R2": 2, "R3": 3, "Unit": 4, "Review": 5}
	sort.SliceStable(lanes, func(i, j int) bool { return order[lanes[i].Phase] < order[lanes[j].Phase] })
	for i := range lanes {
		lane := &lanes[i]
		lane.Tree = stageTree(lane.Children)
		sort.SliceStable(lane.Bars, func(i, j int) bool { return lane.Bars[i].StartedAt.Before(lane.Bars[j].StartedAt) })
		var ends []time.Time
		for _, bar := range lane.Bars {
			track := 0
			for track < len(ends) && !bar.StartedAt.After(ends[track]) {
				track++
			}
			if track == len(ends) {
				ends = append(ends, time.Time{})
				lane.Tracks = append(lane.Tracks, nil)
			}
			lane.Tracks[track] = append(lane.Tracks[track], bar)
			ends[track] = maxStageEnd(bar)
		}
	}
	return lanes
}

func maxStageEnd(row timelineRow) time.Time {
	if row.FinishedAt.After(row.StartedAt) {
		return row.FinishedAt
	}
	return row.StartedAt
}
