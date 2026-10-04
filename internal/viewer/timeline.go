package viewer

import (
	"sort"
	"strings"

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
	Label         string
	OffsetMS      int64
	DurationMS    int64
	MissingParent bool
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
		rows = append(rows, timelineRow{Stage: stage, Label: strings.Repeat("· ", depth) + stage.Name, OffsetMS: stage.StartedAt.Sub(doc.StartedAt).Milliseconds(), DurationMS: stage.Duration(doc.FinishedAt).Milliseconds(), MissingParent: !known[stage.ParentID]})
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
