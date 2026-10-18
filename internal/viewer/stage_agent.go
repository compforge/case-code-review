package viewer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/compforge/agentgo"
	agentsession "github.com/compforge/agentgo/session"
	"github.com/compforge/go-stdx/timeline"
	"github.com/qiankunli/case-code-review/internal/console"
)

// Agent view contains call summaries, not prompt/response copies. Stage IDs
// lead back to the same Default/Model detail and exact Session reference.
type agentView struct {
	StageID    timeline.StageID `json:"stage_id"`
	Turns      []*agentTurnView `json:"turns"`
	Operations []*agentItemView `json:"operations,omitempty"`
	Warnings   []string         `json:"warnings,omitempty"`
}

type agentTurnView struct {
	StageID timeline.StageID `json:"stage_id"`
	Label   string           `json:"label"`
	Status  string           `json:"status"`
	Items   []*agentItemView `json:"items"`
}

type agentItemView struct {
	StageID    timeline.StageID `json:"stage_id"`
	ParentID   timeline.StageID `json:"parent_id"`
	Kind       string           `json:"kind"`
	Depth      int              `json:"depth"`
	Label      string           `json:"label"`
	Status     string           `json:"status"`
	OffsetMS   int64            `json:"offset_ms"`
	DurationMS *int64           `json:"duration_ms,omitempty"`
	Attempt    int              `json:"attempt,omitempty"`
	Ref        agentsession.Ref `json:"ref"`
	Usage      *agentgo.Usage   `json:"usage,omitempty"`
	Error      string           `json:"error,omitempty"`
}

func stageStatus(stage timeline.Stage) string {
	if stage.FinishedAt.IsZero() {
		return "incomplete"
	}
	return string(stage.Status)
}

// +spec=`Agent projection follows recorded turn ancestry, keeps attempts and concurrent calls distinct, and joins usage by the complete Session reference without copying call bodies`
func loadStageAgent(path string, id timeline.StageID) (*agentView, error) {
	source, err := readStageSource(path)
	if err != nil {
		return nil, err
	}
	root, ok := source.byID[id]
	if !ok || root.Name != "execution" {
		return nil, fmt.Errorf("stage has no Agent view: %s", id)
	}
	view := &agentView{StageID: id, Turns: []*agentTurnView{}}
	// A nested execution owns a different loop. It is reachable as an operation,
	// but its calls are not counted as part of the selected loop's turns.
	belongs := func(stage timeline.Stage) bool {
		seen := map[timeline.StageID]bool{}
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
	turns := map[timeline.StageID]*agentTurnView{}
	for _, stage := range source.rows {
		if stage.Name != "turn" || !belongs(stage) {
			continue
		}
		turn := &agentTurnView{StageID: stage.ID, Label: (timelineRow{Stage: stage}).Label(), Status: stageStatus(stage), Items: []*agentItemView{}}
		turns[stage.ID] = turn
		view.Turns = append(view.Turns, turn)
	}
	byFile := map[string]map[agentsession.Ref][]*agentItemView{}
	for _, stage := range source.rows {
		if stage.Name == "turn" || !belongs(stage) {
			continue
		}
		if stage.Name == "tool.queue" || (stage.Name == "tool.invoke" && source.byID[stage.ParentID].Name == "tool.execution") {
			continue
		}
		item := &agentItemView{StageID: stage.ID, ParentID: stage.ParentID, Kind: "operation", Label: (timelineRow{Stage: stage}).Label(), Status: stageStatus(stage), OffsetMS: stage.StartedAt.Sub(root.StartedAt).Milliseconds(), Error: stage.Error}
		if !stage.FinishedAt.IsZero() {
			duration := stage.Duration(time.Time{}).Milliseconds()
			item.DurationMS = &duration
		}
		switch stage.Name {
		case "model.attempt", "llm.request":
			item.Kind = "model"
		case "tool.execution", "tool.invoke":
			item.Kind = "tool"
			if tool, _ := timeline.AttributeValue[string](stage.Attributes, "tool"); tool != "" {
				item.Label = tool
			}
		}
		item.Attempt, _ = timeline.AttributeValue[int](stage.Attributes, "attempt")
		seen := map[timeline.StageID]bool{}
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
		warning, err := readAgentUsage(filepath.Join(strings.TrimSuffix(path, ".jsonl")+".sessions", name), items)
		if err != nil {
			return nil, err
		}
		if warning != "" {
			view.Warnings = append(view.Warnings, warning)
		}
	}
	for _, turn := range view.Turns {
		turn.Items = agentHierarchy(turn.Items, source, turn.StageID)
	}
	view.Operations = agentHierarchy(view.Operations, source, root.ID)
	return view, nil
}

// Use the same tree traversal as Default. Hidden intermediates still contribute
// their recorded ancestry; neither timestamp order nor call kind defines depth.
func agentHierarchy(items []*agentItemView, source *stageSource, root timeline.StageID) []*agentItemView {
	selected := map[timeline.StageID]*agentItemView{}
	included := map[timeline.StageID]bool{}
	for _, item := range items {
		selected[item.StageID] = item
		for stage := source.byID[item.StageID]; stage.ID != "" && stage.ID != root && !included[stage.ID]; stage = source.byID[stage.ParentID] {
			included[stage.ID] = true
		}
	}
	var rows []timelineRow
	for _, stage := range source.rows {
		if included[stage.ID] {
			rows = append(rows, timelineRow{Stage: stage})
		}
	}
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
func readAgentUsage(path string, items map[agentsession.Ref][]*agentItemView) (string, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return "Referenced Session file is missing; call usage is unavailable.", nil
	}
	if err != nil {
		return "", err
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
			return err
		}
		for _, item := range selected {
			if item.Kind == "model" && record.Type == string(agentgo.EventModelExecEnd) && len(data.Message) > 0 {
				response, err := recordedModelMessage(data.Message)
				if err != nil {
					return err
				}
				item.Usage = response.Usage
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
		return "", err
	}
	if result.TruncatedTail {
		return "Session has an incomplete final record; unavailable usage remains unknown.", nil
	}
	return "", nil
}

func handleStageAgent(w http.ResponseWriter, r *http.Request, root, repo, sessionID string) {
	if repo == "" || sessionID == "" || strings.Contains(repo, "..") || strings.Contains(sessionID, "..") || strings.ContainsAny(repo+sessionID, `/\`) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	view, err := loadStageAgent(filepath.Join(root, repo, sessionID+".jsonl"), timeline.StageID(r.URL.Query().Get("stage")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(view); err != nil {
		fmt.Fprintf(console.Err(), "[viewer] stage Agent response: %v\n", err)
	}
}
