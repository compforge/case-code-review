package recording

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	agentview "github.com/compforge/agent-ledger/go/agentgo/observation"
	"github.com/compforge/agent-ledger/go/observation"
	"github.com/compforge/agentgo"
	"github.com/compforge/agentgo/codec"
	agentsession "github.com/compforge/agentgo/session"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/recording/operation"
)

// Jobs lists admitted Units without reading any native Session body.
func (t *Transcript) Jobs() []UnitResult {
	units := map[string]UnitResult{}
	for _, op := range t.Journal.Operations {
		id, _ := operation.AttributeValue[string](op.Attributes, "unit_id")
		if kind, _ := operation.AttributeValue[string](op.Attributes, "kind"); id == "" && kind == "unit" {
			id, _ = operation.AttributeValue[string](op.Attributes, "scope_id")
		}
		if id != "" {
			units[id] = UnitResult{ID: id, Outcome: "running"}
		}
	}
	for _, r := range t.Records {
		if r["type"] == "artifact" && r["artifact_kind"] == "review_unit" {
			data, _ := r["data"].(map[string]any)
			id := textField(data, "unit_id")
			if id != "" {
				u := units[id]
				u.ID = id
				if u.Outcome == "" {
					u.Outcome = "running"
				}
				b, _ := json.Marshal(data["paths"])
				_ = json.Unmarshal(b, &u.Paths)
				units[id] = u
			}
		}
		if r["type"] == "debrief" && r["kind"] == "unit" {
			u := UnitResult{ID: textField(r, "scope_id"), Outcome: textField(r, "outcome"), Reason: textField(r, "reason")}
			b, _ := json.Marshal(r["paths"])
			_ = json.Unmarshal(b, &u.Paths)
			b, _ = json.Marshal(r["unassessed_hypotheses"])
			_ = json.Unmarshal(b, &u.Unassessed)
			if u.ID != "" {
				units[u.ID] = u
			}
		}
	}
	for _, u := range t.Completion().Units {
		units[u.ID] = u
	}
	out := make([]UnitResult, 0, len(units))
	for _, u := range units {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func operationUnit(op operation.Record, index map[operation.ID]operation.Record) string {
	seen := map[operation.ID]bool{}
	for op.ID != "" && !seen[op.ID] {
		seen[op.ID] = true
		if id, ok := operation.AttributeValue[string](op.Attributes, "unit_id"); ok && id != "" {
			return id
		}
		if kind, _ := operation.AttributeValue[string](op.Attributes, "kind"); kind == "unit" {
			id, _ := operation.AttributeValue[string](op.Attributes, "scope_id")
			return id
		}
		op = index[op.ParentID]
	}
	return ""
}
func operationSessionFile(op operation.Record, index map[operation.ID]operation.Record) string {
	seen := map[operation.ID]bool{}
	for op.ID != "" && !seen[op.ID] {
		seen[op.ID] = true
		if name, _ := operation.AttributeValue[string](op.Attributes, "session_file"); name != "" {
			return name
		}
		op = index[op.ParentID]
	}
	return ""
}
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

// ReadJob selects one Unit across R1, R2 and Trial. Shared continuous Sessions
// contribute only explicitly referenced calls, keeping their native identity.
// +spec=`One Unit is one Job; generic operation success and zero Findings never establish business completion`
// +link=component://docs/observability.md
func ReadJob(path, unitID string, options ...codec.Option) (observation.JobView, error) {
	t, err := ReadJournal(path)
	if err != nil {
		return observation.JobView{}, err
	}
	var unit *UnitResult
	for _, u := range t.Jobs() {
		if u.ID == unitID {
			unit = &u
			break
		}
	}
	if unit == nil {
		return observation.JobView{}, fmt.Errorf("unit %q is not recorded", unitID)
	}
	reviewID := t.Journal.ID
	if reviewID == "" {
		for _, record := range t.Records {
			if record["type"] == "session_start" {
				reviewID = textField(record, "sessionId")
				break
			}
		}
	}
	if reviewID == "" {
		return observation.JobView{}, fmt.Errorf("unit %s: missing review identity", unitID)
	}
	view := observation.JobView{SchemaVersion: observation.JobSchemaVersion, Job: observation.JobRef{Source: "ccr", ID: reviewID + "/" + unitID}, Name: unitID, Status: "running", Nodes: observation.Nodes{}, Attributes: map[string]any{"review_id": reviewID, "unit_id": unitID}, Issues: append([]string(nil), t.Gaps...)}
	view.Goal, _ = json.Marshal(map[string]any{"unit_id": unitID, "paths": unit.Paths})
	switch unit.Outcome {
	case "completed":
		if len(unit.Unassessed) == 0 {
			view.Status = "completed"
		} else {
			view.Status = "outcome_unknown"
		}
	case "aborted", "cancelled":
		view.Status = "cancelled"
	case "timeout", "llm_error", "tool_error", "panic", "failed":
		view.Status = "failed"
	case "running":
		if t.Completion().Status != "running" {
			view.Status = "outcome_unknown"
		}
	default:
		view.Status = "outcome_unknown"
	}
	if t.TruncatedTail {
		view.Issues = append(view.Issues, "truncated review journal")
	}
	index := t.OperationIndex()
	selected := map[operation.ID]*observation.Operation{}
	ordered := []operation.ID{}
	for _, op := range t.Journal.Operations {
		if operationUnit(op, index) != unitID {
			continue
		}
		attrs := map[string]any{}
		for k, v := range op.Attributes {
			var value any
			if json.Unmarshal(v, &value) == nil {
				attrs[k] = value
			}
		}
		if op.Error != "" {
			attrs["error"] = op.Error
		}
		selected[op.ID] = &observation.Operation{Node: observation.Node{ID: string(op.ID), Name: op.Name, StartedAt: stamp(op.StartedAt), FinishedAt: stamp(op.FinishedAt), Status: string(op.Status), Attributes: attrs}, Operation: op.Name}
		ordered = append(ordered, op.ID)
		if view.StartedAt == "" || op.StartedAt.Before(parseStamp(view.StartedAt)) {
			view.StartedAt = stamp(op.StartedAt)
		}
		if op.Name == "review.pipeline" {
			view.FinishedAt = stamp(op.FinishedAt)
		}
	}
	// Parentage is explicit, not inferred from time. Broken cycles become roots
	// with a gap, so interrupted or malformed evidence remains inspectable.
	for _, id := range ordered {
		op := index[id]
		parent := selected[op.ParentID]
		cycle := false
		seen := map[operation.ID]bool{id: true}
		for ancestor := op.ParentID; selected[ancestor] != nil; ancestor = index[ancestor].ParentID {
			if seen[ancestor] {
				cycle = true
				break
			}
			seen[ancestor] = true
		}
		if parent != nil && !cycle {
			parent.Nodes = append(parent.Nodes, selected[id])
		} else {
			view.Nodes = append(view.Nodes, selected[id])
			if cycle {
				view.Issues = append(view.Issues, "cyclic operation parentage: "+string(id))
			}
		}
	}
	evidence := []map[string]any{}
	for _, r := range t.Records {
		data, _ := r["data"].(map[string]any)
		owns := selected[operation.ID(textField(r, "operation_id"))] != nil || (r["kind"] == "unit" && r["scope_id"] == unitID) || r["origin_unit"] == unitID || data["unit_id"] == unitID || data["origin_unit"] == unitID
		if owns && r["artifact_kind"] == "review_unit" {
			view.Goal, _ = json.Marshal(data)
		}
		if owns {
			evidence = append(evidence, r)
		}
	}
	view.Result, _ = json.Marshal(map[string]any{"unit": unit, "records": evidence})
	if err := loadJobCalls(path, index, ordered, selected, &view, options...); err != nil {
		return view, err
	}
	inspection, err := view.Inspect()
	if err != nil {
		return view, fmt.Errorf("unit %s JobView: %w", unitID, err)
	}
	view.Issues = append(view.Issues, inspection.Issues...)
	return view, nil
}
func parseStamp(s string) time.Time { t, _ := time.Parse(time.RFC3339Nano, s); return t }

func loadJobCalls(path string, index map[operation.ID]operation.Record, ordered []operation.ID, nodes map[operation.ID]*observation.Operation, view *observation.JobView, options ...codec.Option) error {
	// One content owner per native call. Queue and invocation timing records keep
	// their business position but never duplicate the call body or its usage.
	refs := map[string]operation.ID{}
	files := map[string]bool{}
	for _, id := range ordered {
		op := index[id]
		if op.Name != "model.attempt" && op.Name != "llm.request" && op.Name != "tool.execution" {
			continue
		}
		var ref agentsession.Ref
		if json.Unmarshal(op.Attributes["session_ref"], &ref) != nil || ref.SessionID == "" {
			view.Issues = append(view.Issues, "missing Session reference: "+string(id))
			continue
		}
		name := operationSessionFile(op, index)
		if filepath.Base(name) != name || !strings.HasSuffix(name, ".jsonl") {
			view.Issues = append(view.Issues, "invalid or missing Session file: "+string(id))
			continue
		}
		key := refKey(ref)
		if _, exists := refs[key]; exists {
			view.Issues = append(view.Issues, "multiple operation owners for call: "+key)
			continue
		}
		refs[key] = id
		files[name] = true
	}
	messageCodec, err := msg.Codec(options...)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	found := map[string]bool{}
	for _, name := range names {
		file, err := os.Open(filepath.Join(strings.TrimSuffix(path, ".jsonl")+".sessions", name))
		if err != nil {
			view.Issues = append(view.Issues, fmt.Sprintf("Session %s: %v", name, err))
			continue
		}
		session, err := agentview.ReadSessionView(file, messageCodec)
		file.Close()
		if err != nil {
			view.Issues = append(view.Issues, fmt.Sprintf("Session %s: %v", name, err))
		}
		for _, issue := range session.Issues {
			view.Issues = append(view.Issues, "Session "+name+": "+issue)
		}
		ids := []string{}
		owners := map[string]operation.ID{}
		keysByID := map[string]string{}
		for _, node := range session.Nodes {
			if node.Kind() != "model_call" && node.Kind() != "tool_call" {
				continue
			}
			var exec agentgo.Execution
			raw, _ := json.Marshal(node.Base().Attributes["execution"])
			if json.Unmarshal(raw, &exec) != nil {
				continue
			}
			key := refKey(agentsession.Ref{SessionID: session.Session.ID, RunID: node.Base().RunID, ExecutionID: exec.ID, Attempt: exec.Attempt})
			owner, ok := refs[key]
			if !ok || found[key] {
				continue
			}
			ids = append(ids, node.Base().ID)
			owners[node.Base().ID] = owner
			keysByID[node.Base().ID] = key
		}
		if len(ids) == 0 {
			continue
		}
		selected, err := observation.SelectNodes(session, ids)
		if err != nil {
			view.Issues = append(view.Issues, fmt.Sprintf("Session %s selection: %v", name, err))
			continue
		}
		for _, node := range selected {
			owner := owners[node.Base().ID]
			nodes[owner].Nodes = append(nodes[owner].Nodes, node)
			found[keysByID[node.Base().ID]] = true
		}
	}
	keys := make([]string, 0, len(refs))
	for key := range refs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !found[key] {
			view.Issues = append(view.Issues, "missing native call: "+key)
		}
	}
	return nil
}
