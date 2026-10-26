package viewer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/compforge/agentgo"
	agentsession "github.com/compforge/agentgo/session"
	"github.com/qiankunli/case-code-review/internal/console"
	"github.com/qiankunli/case-code-review/internal/recording/operation"
)

// Agent view contains call summaries, not prompt/response copies. Stage IDs
// lead back to the same Default/Model detail and exact Session reference.
type agentView struct {
	OperationID operation.ID     `json:"operation_id"`
	Turns       []*agentTurnView `json:"turns"`
	Operations  []*agentItemView `json:"operations,omitempty"`
	Warnings    []string         `json:"warnings,omitempty"`
}

type agentTurnView struct {
	OffsetMS    int64            `json:"offset_ms"`
	DurationMS  *int64           `json:"duration_ms,omitempty"`
	OperationID operation.ID     `json:"operation_id"`
	Label       string           `json:"label"`
	Status      string           `json:"status"`
	Items       []*agentItemView `json:"items"`
}

type agentItemView struct {
	OperationID operation.ID     `json:"operation_id"`
	ParentID    operation.ID     `json:"parent_id"`
	Kind        string           `json:"kind"`
	Depth       int              `json:"depth"`
	Label       string           `json:"label"`
	Status      string           `json:"status"`
	OffsetMS    int64            `json:"offset_ms"`
	DurationMS  *int64           `json:"duration_ms,omitempty"`
	Attempt     int              `json:"attempt,omitempty"`
	Ref         agentsession.Ref `json:"ref"`
	Usage       *agentgo.Usage   `json:"usage,omitempty"`
	Error       string           `json:"error,omitempty"`
}

func stageStatus(stage operation.Record) string {
	if stage.FinishedAt.IsZero() {
		return "incomplete"
	}
	return string(stage.Status)
}

// +spec=`Agent projection follows recorded turn ancestry, keeps attempts and concurrent calls distinct, and joins usage by the complete Session reference without copying call bodies`
func loadStageAgent(path string, id operation.ID) (*agentView, error) {
	source, err := readStageSource(path)
	if err != nil {
		return nil, err
	}
	root, ok := source.byID[id]
	if !ok || root.Name != "execution" {
		return nil, fmt.Errorf("stage has no Agent view: %s", id)
	}
	view := &agentView{OperationID: id, Turns: []*agentTurnView{}}
	// A nested execution owns a different loop. It is reachable as an operation,
	// but its calls are not counted as part of the selected loop's turns.
	belongs := func(stage operation.Record) bool {
		seen := map[operation.ID]bool{}
		for parent := source.byID[stage.ParentID]; parent.ID != "" && !seen[parent.ID]; parent = source.byID[parent.ParentID] {
			if parent.ID == id {
				return true
			}
			if parent.Name == "execution" {
				return false
			}
			seen[parent.ID] = true
		}
		return false
	}
	turns := map[operation.ID]*agentTurnView{}
	for _, stage := range source.rows {
		if stage.Name != "turn" || !belongs(stage) {
			continue
		}
		turn := &agentTurnView{OperationID: stage.ID, Label: (timelineRow{Record: stage}).Label(), Status: stageStatus(stage), Items: []*agentItemView{}}
		turn.OffsetMS = stage.StartedAt.Sub(root.StartedAt).Milliseconds()
		if !stage.FinishedAt.IsZero() {
			duration := stage.Duration(time.Time{}).Milliseconds()
			turn.DurationMS = &duration
		}
		turns[stage.ID] = turn
		view.Turns = append(view.Turns, turn)
	}
	byFile := map[string]map[agentsession.Ref][]*agentItemView{}
	stageOrder := map[operation.ID]int{}
	for index, stage := range source.rows {
		if stage.Name == "turn" || !belongs(stage) {
			continue
		}
		stageOrder[stage.ID] = index
		if stage.Name == "tool.queue" || (stage.Name == "tool.invoke" && source.byID[stage.ParentID].Name == "tool.execution") {
			continue
		}
		item := &agentItemView{OperationID: stage.ID, ParentID: stage.ParentID, Kind: "operation", Label: (timelineRow{Record: stage}).Label(), Status: stageStatus(stage), OffsetMS: stage.StartedAt.Sub(root.StartedAt).Milliseconds(), Error: stage.Error}
		if !stage.FinishedAt.IsZero() {
			duration := stage.Duration(time.Time{}).Milliseconds()
			item.DurationMS = &duration
		}
		switch stage.Name {
		case "model.attempt", "llm.request":
			item.Kind = "model"
		case "tool.execution", "tool.invoke":
			item.Kind = "tool"
			if tool, _ := operation.AttributeValue[string](stage.Attributes, "tool"); tool != "" {
				item.Label = tool
			}
		}
		item.Attempt, _ = operation.AttributeValue[int](stage.Attributes, "attempt")
		seen := map[operation.ID]bool{}
		var turn *agentTurnView
		for parent := source.byID[stage.ParentID]; parent.ID != "" && parent.ID != id && !seen[parent.ID]; parent = source.byID[parent.ParentID] {
			seen[parent.ID] = true
			if turn = turns[parent.ID]; turn != nil {
				break
			}
		}
		if turn != nil {
			turn.Items = append(turn.Items, item)
		} else {
			view.Operations = append(view.Operations, item)
		}
		if item.Kind == "operation" {
			continue
		}
		if json.Unmarshal(stage.Attributes["session_ref"], &item.Ref) != nil || item.Ref.SessionID == "" || item.Ref.RunID == "" || item.Ref.ExecutionID == "" || item.Ref.Attempt < 1 {
			view.Warnings = append(view.Warnings, fmt.Sprintf("Stage %s has no complete Session reference.", stage.ID))
			continue
		}
		item.Attempt = item.Ref.Attempt
		name, err := source.sessionFile(stage)
		if err != nil {
			return nil, err
		}
		if name == "" {
			view.Warnings = append(view.Warnings, fmt.Sprintf("Stage %s has no Session file.", stage.ID))
			continue
		}
		if byFile[name] == nil {
			byFile[name] = map[agentsession.Ref][]*agentItemView{}
		}
		byFile[name][item.Ref] = append(byFile[name][item.Ref], item)
	}
	for name, items := range byFile {
		view.Warnings = append(view.Warnings, readAgentUsage(filepath.Join(strings.TrimSuffix(path, ".jsonl")+".sessions", name), items)...)
	}
	for _, turn := range view.Turns {
		turn.Items = agentHierarchy(turn.Items, source, turn.OperationID, stageOrder)
	}
	view.Operations = agentHierarchy(view.Operations, source, root.ID, stageOrder)
	return view, nil
}

// Use the same tree traversal as Default. Hidden intermediates still contribute
// their recorded ancestry; neither timestamp order nor call kind defines depth.
// +spec=`Each turn collects only its own items and ancestors; the shared source order is indexed once rather than rescanning the review timeline for every turn`
func agentHierarchy(items []*agentItemView, source *stageSource, root operation.ID, stageOrder map[operation.ID]int) []*agentItemView {
	selected := map[operation.ID]*agentItemView{}
	included := map[operation.ID]bool{}
	var rows []timelineRow
	for _, item := range items {
		selected[item.OperationID] = item
		for stage := source.byID[item.OperationID]; stage.ID != "" && stage.ID != root && !included[stage.ID]; stage = source.byID[stage.ParentID] {
			included[stage.ID] = true
			rows = append(rows, timelineRow{Record: stage})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return stageOrder[rows[i].ID] < stageOrder[rows[j].ID] })
	ordered := make([]*agentItemView, 0, len(items))
	var visit func(*timelineBranch)
	visit = func(branch *timelineBranch) {
		if item := selected[branch.ID]; item != nil {
			item.Depth = branch.Depth + 1
			ordered = append(ordered, item)
		}
		for _, child := range branch.Children {
			visit(child)
		}
	}
	for _, branch := range stageTree(rows) {
		visit(branch)
	}
	return ordered
}

// Read each linked Session once; unrelated runs and attempts never supply usage.
// +spec=`Session read failures preserve the Agent tree and valid usage; unavailable usage remains unknown and diagnostics identify the affected file or record`
func readAgentUsage(path string, items map[agentsession.Ref][]*agentItemView) []string {
	var warnings []string
	warn := func(reason string, err error) {
		warnings = append(warnings, fmt.Sprintf("Session %s: %s", filepath.Base(path), reason))
		if err != nil {
			fmt.Fprintf(console.Err(), "[viewer] Agent Session %s: %s: %v\n", path, reason, err)
		}
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		warn("file is missing; call usage is unavailable.", nil)
		return warnings
	}
	if err != nil {
		warn("cannot open file; call usage is unavailable.", err)
		return warnings
	}
	defer file.Close()
	result, err := agentsession.Read(file, func(record agentsession.Record) error {
		if record.Execution == nil || len(record.Data) == 0 {
			return nil
		}
		ref := agentsession.Ref{SessionID: record.SessionID, RunID: record.RunID, ExecutionID: record.Execution.ID, Attempt: record.Execution.Attempt}
		selected := items[ref]
		if len(selected) == 0 {
			return nil
		}
		if record.Type != string(agentgo.EventModelExecEnd) && record.Type != string(agentgo.EventToolExecEnd) {
			return nil
		}
		var data agentsession.EventData
		if err := json.Unmarshal(record.Data, &data); err != nil {
			warn(fmt.Sprintf("record %d has invalid event data; skipped.", record.Seq), err)
			return nil
		}
		if len(data.PartialMessage) > 0 {
			data.Message = data.PartialMessage
		}
		var usage *agentgo.Usage
		if record.Type == string(agentgo.EventModelExecEnd) && len(data.Message) > 0 {
			response, err := recordedModelMessage(data.Message)
			if err != nil {
				warn(fmt.Sprintf("record %d has an invalid model response; usage is unavailable.", record.Seq), err)
			} else {
				usage = response.Usage
			}
		}
		for _, item := range selected {
			if item.Kind == "model" && usage != nil {
				item.Usage = usage
			}
			if item.Kind == "tool" && data.Tool != "" {
				item.Label = data.Tool
			}
			if data.Error != "" {
				item.Error = data.Error
			}
		}
		return nil
	})
	if err != nil {
		warn("read incomplete; unread call usage remains unknown.", err)
	}
	if result.TruncatedTail {
		warn("incomplete final record; unavailable usage remains unknown.", nil)
	}
	return warnings
}

func handleStageAgent(w http.ResponseWriter, r *http.Request, root, repo, sessionID string) {
	if repo == "" || sessionID == "" || strings.Contains(repo, "..") || strings.Contains(sessionID, "..") || strings.ContainsAny(repo+sessionID, `/\`) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	view, err := loadStageAgent(filepath.Join(root, repo, sessionID+".jsonl"), operation.ID(r.URL.Query().Get("stage")))
	if err != nil {
		fmt.Fprintf(console.Err(), "[viewer] Agent view repo=%s review=%s stage=%s: %v\n", repo, sessionID, r.URL.Query().Get("stage"), err)
		http.Error(w, "Agent view unavailable: review timeline or loop stage could not be loaded.", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(view); err != nil {
		fmt.Fprintf(console.Err(), "[viewer] stage Agent response: %v\n", err)
	}
}
