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

	agentview "github.com/compforge/agent-ledger/go/agentgo/observation"
	"github.com/compforge/agent-ledger/go/common"
	"github.com/compforge/agent-ledger/go/observation"
	"github.com/compforge/agentgo"
	"github.com/compforge/agentgo/codec"
	agentsession "github.com/compforge/agentgo/session"
	"github.com/qiankunli/case-code-review/internal/console"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/recording/operation"
)

type nativeSession struct {
	ID         string             `json:"id"`
	File       string             `json:"-"`
	Role       string             `json:"role"`
	Lane       string             `json:"lane,omitempty"`
	Units      []string           `json:"units"`
	Executions []operation.Record `json:"executions"`
}

func attributes(source *callSource, op operation.Record) map[string]json.RawMessage {
	result := map[string]json.RawMessage{}
	seen := map[operation.ID]bool{}
	for op.ID != "" && !seen[op.ID] {
		seen[op.ID] = true
		for key, value := range op.Attributes {
			if _, ok := result[key]; !ok {
				result[key] = value
			}
		}
		op = source.byID[op.ParentID]
	}
	return result
}
func attr(attrs map[string]json.RawMessage, key string) string {
	value, _ := operation.AttributeValue[string](attrs, key)
	return value
}

func unitIdentity(a map[string]json.RawMessage) string {
	if id := attr(a, "unit_id"); id != "" {
		return id
	}
	if id := attr(a, "origin_unit"); id != "" {
		return id
	}
	if attr(a, "kind") == "unit" {
		return attr(a, "scope_id")
	}
	return ""
}

// +spec=`Run-level events bind to a common enclosing Execution, never a representative call; missing or conflicting Execution, Unit or Hypothesis ownership remains unknown`
// +link=component://docs/observability.md
func runExecutionOwners(source *callSource, calls map[agentsession.Ref]operation.Record) map[string]operation.Record {
	result := map[string]operation.Record{}
	ambiguous := map[string]bool{}
	for ref, call := range calls {
		if ref.RunID == "" {
			continue
		}
		owner := source.enclosingExecution(call)
		callAttrs, ownerAttrs := attributes(source, call), attributes(source, owner)
		if owner.ID == "" || unitIdentity(callAttrs) != unitIdentity(ownerAttrs) || attr(callAttrs, "hypothesis_id") != attr(ownerAttrs, "hypothesis_id") {
			ambiguous[ref.RunID] = true
			continue
		}
		if previous, ok := result[ref.RunID]; ok && previous.ID != owner.ID {
			ambiguous[ref.RunID] = true
		}
		result[ref.RunID] = owner
	}
	for run := range ambiguous {
		delete(result, run)
	}
	return result
}

// Session membership comes from native references, never from overlapping timestamps.
// +spec=`A continuous Lane has one Session entry; Execution boundaries annotate it without splitting its identity`
// +link=component://docs/observability.md
func sessionCatalog(source *callSource) ([]nativeSession, error) {
	result := []nativeSession{}
	positions := map[string]int{}
	for _, op := range source.rows {
		a := attributes(source, op)
		id := attr(a, "session_id")
		var ref agentsession.Ref
		if json.Unmarshal(op.Attributes["session_ref"], &ref) == nil && ref.SessionID != "" {
			id = ref.SessionID
		}
		if id == "" {
			continue
		}
		file, err := source.sessionFile(op)
		if err != nil {
			return nil, err
		}
		if file == "" {
			continue
		}
		i, ok := positions[id]
		if !ok {
			i = len(result)
			positions[id] = i
			result = append(result, nativeSession{ID: id, File: file, Role: "Auxiliary", Units: []string{}, Executions: []operation.Record{}})
		}
		entry := &result[i]
		if entry.File != file {
			return nil, fmt.Errorf("Session %s has conflicting files", id)
		}
		if lane := attr(a, "lane_id"); lane != "" {
			entry.Role = "R2 Lane"
			entry.Lane = lane
		} else if attr(a, "kind") == "lane" {
			entry.Role = "R2 Lane"
			entry.Lane = strings.TrimPrefix(attr(a, "scope_id"), "hypothesis_review:")
		} else if attr(a, "task_type") == "main_task" {
			entry.Role = "R1"
		}
		unit := unitIdentity(a)
		if unit != "" {
			found := false
			for _, u := range entry.Units {
				found = found || u == unit
			}
			if !found {
				entry.Units = append(entry.Units, unit)
			}
		}
		if op.Name == "execution" {
			entry.Executions = append(entry.Executions, op)
		}
	}
	order := map[string]int{"R1": 0, "R2 Lane": 1, "Auxiliary": 2}
	sort.SliceStable(result, func(i, j int) bool { return order[result[i].Role] < order[result[j].Role] })
	return result, nil
}

type sessionItem struct {
	Searches []searchResult `json:"searches,omitempty"`
	observation.Node
	Kind        string           `json:"kind"`
	Ref         agentsession.Ref `json:"ref"`
	OperationID operation.ID     `json:"operation_id,omitempty"`
	ExecutionID string           `json:"execution_id,omitempty"`
	Unit        string           `json:"unit,omitempty"`
	Hypothesis  string           `json:"hypothesis,omitempty"`
	Summary     string           `json:"summary,omitempty"`
	DurationMS  *int64           `json:"duration_ms,omitempty"`
	Usage       *common.Usage    `json:"usage,omitempty"`
	Error       string           `json:"error,omitempty"`
	ModelCallID string           `json:"model_call_id,omitempty"`
	ToolCallID  string           `json:"tool_call_id,omitempty"`
	Previous    string           `json:"previous,omitempty"`
}
type sessionPage struct {
	Session nativeSession `json:"session"`
	Items   []sessionItem `json:"items"`
	Issues  []string      `json:"issues"`
}

func readNativeSession(path, id string, options ...codec.Option) (nativeSession, observation.SessionView, *callSource, error) {
	source, err := readCallSource(path)
	if err != nil {
		return nativeSession{}, observation.SessionView{}, nil, err
	}
	catalog, err := sessionCatalog(source)
	if err != nil {
		return nativeSession{}, observation.SessionView{}, nil, err
	}
	var selected *nativeSession
	for _, entry := range catalog {
		if entry.ID == id {
			selected = &entry
			break
		}
	}
	if selected == nil {
		return nativeSession{}, observation.SessionView{}, nil, fmt.Errorf("Session not associated with Review")
	}
	messageCodec, err := msg.Codec(options...)
	if err != nil {
		return *selected, observation.SessionView{}, source, err
	}
	file, err := os.Open(filepath.Join(strings.TrimSuffix(path, ".jsonl")+".sessions", selected.File))
	if err != nil {
		fmt.Fprintf(console.Err(), "[viewer] open Session review=%s session=%s: %v\n", filepath.Base(path), id, err)
		return *selected, observation.SessionView{Issues: []string{"Referenced Session file is unavailable."}}, source, nil
	}
	defer file.Close()
	view, err := agentview.ReadSessionView(file, messageCodec)
	if err != nil {
		fmt.Fprintf(console.Err(), "[viewer] read Session review=%s session=%s: %v\n", filepath.Base(path), id, err)
	}
	if view.Session.ID != "" && view.Session.ID != id {
		return *selected, observation.SessionView{}, source, fmt.Errorf("recorded Session identity mismatch")
	}
	return *selected, view, source, nil
}

// Native call IDs survive filtering and cross-view navigation. Body loading is a
// separate query, so a Session summary never serializes the full Lane context.
// +spec=`Session summaries include every native run and attempt once, retain missing outcomes and use recorded intervals only`
func summarizeSession(entry nativeSession, view observation.SessionView, source *callSource) sessionPage {
	result := sessionPage{Session: entry, Items: []sessionItem{}, Issues: view.Issues}
	owners := map[agentsession.Ref]operation.Record{}
	for _, op := range source.rows {
		if op.Name != "model.attempt" && op.Name != "llm.request" && op.Name != "tool.execution" {
			continue
		}
		var ref agentsession.Ref
		if json.Unmarshal(op.Attributes["session_ref"], &ref) == nil && ref.SessionID == entry.ID {
			owners[ref] = op
		}
	}
	runOwners := runExecutionOwners(source, owners)
	modelByTool := map[string]string{}
	for _, node := range view.Nodes {
		model, ok := node.(*observation.ModelCall)
		if !ok || model.Response == nil || model.Status != "completed" {
			continue
		}
		message, err := recordedModelMessage(model.Response.Output)
		if err != nil {
			continue
		}
		for _, call := range message.ToolCalls() {
			key := model.RunID + "/" + call.ID
			if _, exists := modelByTool[key]; exists {
				modelByTool[key] = ""
			} else {
				modelByTool[key] = model.ID
			}
		}
	}
	previous := ""
	for _, node := range view.Nodes {
		kind := node.Kind()
		base := *node.Base()
		item := sessionItem{Node: base, Kind: kind}
		item.Attributes = nil
		var execution agentgo.Execution
		raw, _ := json.Marshal(base.Attributes["execution"])
		_ = json.Unmarshal(raw, &execution)
		item.Ref = agentsession.Ref{SessionID: entry.ID, RunID: base.RunID, ExecutionID: execution.ID, Attempt: execution.Attempt}
		owner := owners[item.Ref]
		if owner.ID == "" && kind == "operation" {
			owner = runOwners[base.RunID]
		}
		a := attributes(source, owner)
		item.OperationID = owner.ID
		item.ExecutionID = attr(a, "execution_id")
		item.Unit = unitIdentity(a)
		item.Hypothesis = attr(a, "hypothesis_id")
		var response *common.CallResponse
		switch n := node.(type) {
		case *observation.ModelCall:
			item.Name = "Model call"
			item.Previous = previous
			previous = base.ID
			response = n.Response
			if response != nil && len(response.Output) > 0 {
				if m, err := recordedModelMessage(response.Output); err == nil {
					item.Summary = truncateText(240, m.TextContent())
					if item.Summary == "" && len(m.ToolCalls()) > 0 {
						names := []string{}
						for _, c := range m.ToolCalls() {
							names = append(names, c.Name)
						}
						item.Summary = strings.Join(names, ", ")
					}
				}
			}
		case *observation.ToolCall:
			item.Name = "Tool call"
			item.ToolCallID = n.ToolCallID
			item.ModelCallID = n.ModelCallID
			if item.ModelCallID == "" {
				item.ModelCallID = modelByTool[n.RunID+"/"+n.ToolCallID]
			}
			item.Searches = searchResults(n)
			for i := range item.Searches {
				item.Searches[i].Evidence = ""
			}
			response = n.Response
			if n.Request != nil {
				item.Name = n.Request.ToolName
			}
		case *observation.Operation:
			// Streaming fragments are body evidence, not separate rows. Context changes
			// and run boundaries remain visible even when no business Operation joined.
			if !strings.Contains(n.Operation, "context_compact") && !strings.Contains(n.Operation, "run_") && !strings.Contains(n.Operation, "agent_start") && !strings.Contains(n.Operation, "agent_end") {
				continue
			}
			item.Name = strings.TrimPrefix(n.Operation, "agentgo.")
			if strings.Contains(n.Operation, "context_compact") {
				raw, _ := json.Marshal(n.Data)
				var event agentsession.EventData
				if json.Unmarshal(raw, &event) == nil && event.Compaction != nil {
					c := event.Compaction
					item.Summary = fmt.Sprintf("%s · committed=%t · messages %d → %d · tokens %d → %d", c.Reason, c.Committed, c.MessagesBefore, c.MessagesAfter, c.TokensBefore, c.TokensAfter)
				}
			}
		default:
			continue
		}
		if response != nil {
			item.Usage = response.Usage
			if response.Error != nil {
				item.Error = response.Error.Message
			}
		}
		if item.Status == "" {
			item.Status = "unknown"
		}
		start, e1 := time.Parse(time.RFC3339Nano, base.StartedAt)
		end, e2 := time.Parse(time.RFC3339Nano, base.FinishedAt)
		if e1 == nil && e2 == nil && !end.Before(start) {
			duration := end.Sub(start).Milliseconds()
			item.DurationMS = &duration
		}
		result.Items = append(result.Items, item)
	}
	return result
}

func handleNativeSession(w http.ResponseWriter, r *http.Request, root, repo, review string, options ...codec.Option) {
	if repo == "" || review == "" || strings.Contains(repo+review, "..") || strings.ContainsAny(repo+review, `/\`) {
		http.Error(w, "invalid path", 400)
		return
	}
	entry, view, source, err := readNativeSession(filepath.Join(root, repo, review+".jsonl"), r.URL.Query().Get("session"), options...)
	if err != nil {
		fmt.Fprintf(console.Err(), "[viewer] Session repo=%s review=%s session=%s: %v\n", repo, review, r.URL.Query().Get("session"), err)
		http.Error(w, "Session unavailable", 404)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	var response any
	if id := r.URL.Query().Get("node"); id != "" {
		for _, node := range view.Nodes {
			if node.Base().ID != id {
				continue
			}
			// Raw record attributes duplicate complete requests; the typed call owns its body.
			node.Base().Attributes = nil
			detail := map[string]any{"kind": node.Kind(), "node": node, "issues": view.Issues}
			if tool, ok := node.(*observation.ToolCall); ok {
				detail["searches"] = searchResults(tool)
			}
			response = detail
			if model, ok := node.(*observation.ModelCall); ok && model.Response != nil && len(model.Response.Output) > 0 {
				if m, e := recordedModelMessage(model.Response.Output); e == nil {
					model.Response.Output, _ = json.Marshal(m)
				}
			}
			break
		}
		if response == nil {
			http.Error(w, "Node not recorded in this Session", 404)
			return
		}
	} else {
		response = summarizeSession(entry, view, source)
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		fmt.Fprintf(console.Err(), "[viewer] Session response %s: %v\n", entry.ID, err)
	}
}
