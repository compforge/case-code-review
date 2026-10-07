package viewer

import (
	"sort"
	"time"

	"github.com/compforge/go-stdx/timeline"
)

// Request cards are views of the Session timeline, never independent recordings.
func (vs *ViewSession) projectRequestTimelines() {
	if vs.Timeline == nil {
		return
	}
	children := map[timeline.StageID][]timeline.StageUpdate{}
	for _, stage := range vs.Timeline.Stages {
		children[stage.ParentID] = append(children[stage.ParentID], stage)
	}
	for _, stage := range vs.Timeline.Stages {
		if stage.Name != "llm.request" {
			continue
		}

		doc := timeline.Document{ID: string(stage.ID), RootStageID: stage.ID, OperationRecord: timeline.OperationRecord{Operation: stage.Name, StartedAt: stage.StartedAt, FinishedAt: stage.FinishedAt, Status: stage.Status, Error: stage.Error}}
		var appendChildren func(timeline.StageID)
		seen := map[timeline.StageID]bool{}
		appendChildren = func(id timeline.StageID) {
			if seen[id] {
				return
			}
			seen[id] = true
			for _, child := range children[id] {
				doc.Stages = append(doc.Stages, child)
				appendChildren(child.ID)
			}
		}
		appendChildren(stage.ID)
		for _, scope := range vs.Reviews {
			for _, card := range scope.Calls {
				if card.StageID == string(stage.ID) {
					card.Timeline = &doc
				}
			}
		}
	}
}

type timelineRow struct {
	timeline.Stage
	OffsetMS      int64
	DurationMS    int64
	MissingParent bool
	Depth         int
	HasChildren   bool
	StartPercent  float64
	WidthPercent  float64
}

// Layout preserves hierarchy and overlapping intervals; it never sums durations.
func timelineRows(doc *timeline.Document) []timelineRow {
	if doc == nil {
		return nil
	}
	stages := append([]timeline.StageUpdate(nil), doc.Stages...)
	sort.SliceStable(stages, func(i, j int) bool {
		if stages[i].StartedAt.Equal(stages[j].StartedAt) {
			return stages[i].ID < stages[j].ID
		}
		return stages[i].StartedAt.Before(stages[j].StartedAt)
	})
	known := map[timeline.StageID]bool{doc.RootStageID: true}
	children := map[timeline.StageID][]timeline.Stage{}
	for _, stage := range stages {
		known[stage.ID] = true
		children[stage.ParentID] = append(children[stage.ParentID], stage.Stage)
	}
	seen := map[timeline.StageID]bool{}
	var rows []timelineRow
	var visit func(timeline.Stage, int)
	visit = func(stage timeline.Stage, depth int) {
		if seen[stage.ID] {
			return
		}
		seen[stage.ID] = true
		rows = append(rows, timelineRow{Stage: stage, OffsetMS: stage.StartedAt.Sub(doc.StartedAt).Milliseconds(), DurationMS: stage.Duration(doc.FinishedAt).Milliseconds(), MissingParent: !known[stage.ParentID], Depth: depth, HasChildren: len(children[stage.ID]) > 0})
		for _, child := range children[stage.ID] {
			visit(child, depth+1)
		}
	}
	for _, stage := range stages {
		if stage.ParentID == doc.RootStageID || !known[stage.ParentID] {
			visit(stage.Stage, 0)
		}
	}
	for _, stage := range stages {
		if !seen[stage.ID] {
			visit(stage.Stage, 0)
		}
	}
	return rows
}

type timelineView struct {
	Rows       []timelineRow
	WindowMS   int64
	Incomplete int
}

// spec: Bars share source timestamps; unknown end times are markers, never
// intervals extended to the current wall clock. Elapsed is kept for the label.
func layoutTimeline(doc *timeline.Document) timelineView {
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
	return view
}
