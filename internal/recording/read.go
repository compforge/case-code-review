package recording

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/compforge/agentgo"
	agentsession "github.com/compforge/agentgo/session"
	"github.com/compforge/go-stdx/timeline"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/timelinelog"
)

// ReadReview joins the review manifest, incremental timeline and local Session
// records. Request/response maps are a query projection, never a second archive.
func ReadReview(path string) (*Transcript, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	t, err := ReadTranscript(file)
	file.Close()
	if err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(path, ".jsonl")
	file, err = os.Open(base + ".timeline")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		doc, at, torn, e := timelinelog.Read(file)
		file.Close()
		if e != nil {
			return nil, e
		}
		t.Timeline = doc.Snapshot(at)
		t.TruncatedTail = t.TruncatedTail || torn
	}
	paths, err := filepath.Glob(filepath.Join(base+".sessions", "*.jsonl"))
	if err != nil {
		return nil, err
	}
	found := map[string]bool{}
	for _, local := range paths {
		found[filepath.Base(local)] = true
		if err = t.readLoop(local); err != nil {
			t.Gaps = append(t.Gaps, fmt.Sprintf("%s: %v", filepath.Base(local), err))
		}
	}
	expected := map[string]bool{}
	for _, stage := range t.Timeline.Stages {
		var name string
		if json.Unmarshal(stage.Attributes["session_file"], &name) == nil && name != "" {
			expected[name] = true
		}
	}
	for name := range expected {
		if !found[name] {
			t.Gaps = append(t.Gaps, "missing local Session: "+name)
		}
	}
	if t.Timeline.ID == "" {
		t.Gaps = append(t.Gaps, "missing review timeline")
	}
	sort.Strings(t.Gaps)
	sort.SliceStable(t.Records, func(i, j int) bool {
		return textField(t.Records[i], "timestamp") < textField(t.Records[j], "timestamp")
	})
	return t, nil
}

func refKey(r agentsession.Ref) string {
	return fmt.Sprintf("%s/%s/%s/%d", r.SessionID, r.RunID, r.ExecutionID, r.Attempt)
}
func textField(m map[string]any, key string) string { v, _ := m[key].(string); return v }

func (t *Transcript) readLoop(path string) error {
	codec, err := msg.Codec()
	if err != nil {
		return err
	}
	index := t.StageIndex()
	refs := map[string]timeline.Stage{}
	owners := map[string]timeline.Stage{}
	for _, stage := range t.Timeline.Stages {
		var ref agentsession.Ref
		if json.Unmarshal(stage.Attributes["session_ref"], &ref) != nil || ref.SessionID == "" {
			continue
		}
		// Queue/invoke share a call reference; execution is the content boundary.
		if stage.Name == "model.attempt" || stage.Name == "tool.execution" || stage.Name == "llm.request" {
			refs[refKey(ref)] = stage
		}
		owner := stage
		visited := map[timeline.StageID]bool{}
		for owner.Name != "execution" && index[owner.ParentID].ID != "" && !visited[owner.ID] {
			visited[owner.ID] = true
			owner = index[owner.ParentID]
		}
		owners[ref.SessionID+"/"+ref.RunID] = owner
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	rounds := map[string]int{}
	projections := map[string]int{}
	calls := map[string]string{}
	args := map[string]string{}
	ended := map[string]bool{}
	result, err := agentsession.Read(file, func(record agentsession.Record) error {
		if len(record.Data) == 0 {
			return nil
		}
		var data agentsession.EventData
		if err := json.Unmarshal(record.Data, &data); err != nil {
			return err
		}
		ref := agentsession.Ref{SessionID: record.SessionID, RunID: record.RunID}
		if record.Execution != nil {
			ref.ExecutionID = record.Execution.ID
			ref.Attempt = record.Execution.Attempt
		}
		stage := refs[refKey(ref)]
		owner := owners[record.SessionID+"/"+record.RunID]
		if stage.ID != "" {
			owner = stage
		}
		facts := map[string]any{}
		visited := map[timeline.StageID]bool{}
		for owner.ID != "" && !visited[owner.ID] {
			visited[owner.ID] = true
			for key, raw := range owner.Attributes {
				if _, ok := facts[key]; !ok {
					var v any
					if json.Unmarshal(raw, &v) == nil {
						facts[key] = v
					}
				}
			}
			owner = index[owner.ParentID]
		}
		facts["taskType"] = facts["task_type"]
		facts["session_ref"] = ref
		facts["call_id"] = refKey(ref)
		facts["session_file"] = filepath.Base(path)
		facts["sessionId"] = t.Timeline.ID
		facts["timeline_id"] = t.Timeline.ID
		facts["stage_id"] = string(stage.ID)
		facts["timestamp"] = record.Timestamp.Format("2006-01-02T15:04:05.999999999Z07:00")
		facts["uuid"] = fmt.Sprintf("%s/%d", record.SessionID, record.Seq)
		callKey := record.RunID + "/" + data.ToolID
		executionKey := textField(facts, "execution_id")
		switch agentgo.EventType(record.Type) {
		case agentgo.EventModelRequest:
			if data.ModelRequest == nil {
				return nil
			}
			rounds[executionKey]++
			facts["type"] = "llm_request"
			facts["request_no"] = rounds[executionKey]
			facts["messages"] = wireMessages(data.ModelRequest.Messages)
			facts["tools"] = data.ModelRequest.Tools
		case agentgo.EventModelExecEnd:
			ended[refKey(ref)] = true
			if data.Error != "" {
				facts["type"] = "llm_error"
				facts["error"] = data.Error
				break
			}
			if len(data.Message) == 0 {
				return nil
			}
			var message agentgo.AgentMessage
			if err := codec.Unmarshal(data.Message, &message); err != nil {
				return err
			}
			m, _ := message.ToMessage()
			facts["type"] = "llm_response"
			facts["content"] = m.TextContent()
			facts["reasoning"] = m.ThinkingContent()
			facts["stop_reason"] = m.StopReason
			facts["model"] = m.Metadata["model"]
			facts["usage_source"] = "missing"
			if u := m.Usage; u != nil {
				facts["model"] = u.Model
				facts["usage_source"] = "reported"
				facts["usage"] = TokenUsage{PromptTokens: u.Input + u.CacheWrite, CompletionTokens: u.Output, CacheReadTokens: u.CacheRead, CacheWriteTokens: u.CacheWrite}
			}
			toolCalls := []map[string]any{}
			for _, call := range m.ToolCalls() {
				argument := string(call.Args)
				if call.ArgsInvalid {
					argument = call.ArgsRawText
				}
				toolCalls = append(toolCalls, map[string]any{"id": call.ID, "name": call.Name, "arguments": argument})
				calls[record.RunID+"/"+call.ID] = CallID(facts)
				args[record.RunID+"/"+call.ID] = argument
			}
			facts["tool_calls"] = toolCalls
		case agentgo.EventToolExecEnd:
			ended[refKey(ref)] = true
			facts["type"] = "tool_result"
			facts["tool_name"] = data.Tool
			facts["tool_call_id"] = data.ToolID
			facts["request_id"] = calls[callKey]
			facts["arguments"] = args[callKey]
			facts["ok"] = !data.IsError
			if data.ToolResult != nil {
				raw := data.ToolResult.Content
				var text string
				if json.Unmarshal(raw, &text) != nil {
					text = string(raw)
				}
				facts["result"] = text
				facts["metadata"] = data.ToolResult.Details
			}
		case agentgo.EventContextProjected:
			projections[executionKey]++
			facts["type"] = "context_projected"
			facts["projection_no"] = projections[executionKey]
			facts["items"] = data.ContextItems
		case agentgo.EventContextCompacted:
			if data.Compaction == nil {
				return nil
			}
			facts["type"] = "context_compacted"
			c := data.Compaction
			facts["reason"] = c.Reason
			facts["committed"] = c.Committed
			facts["tokens_before"] = c.TokensBefore
			facts["tokens_after"] = c.TokensAfter
			facts["messages_before"] = c.MessagesBefore
			facts["messages_after"] = c.MessagesAfter
			facts["summarized"] = c.Summarized
		default:
			return nil
		}
		// Keep the existing consumer DTO numeric/map shape across all query surfaces.
		raw, err := json.Marshal(facts)
		if err != nil {
			return err
		}
		var normalized map[string]any
		if err = json.Unmarshal(raw, &normalized); err != nil {
			return err
		}
		t.Records = append(t.Records, normalized)
		return nil
	})
	for key, stage := range refs {
		var ref agentsession.Ref
		_ = json.Unmarshal(stage.Attributes["session_ref"], &ref)
		if ref.SessionID == result.SessionID && !stage.FinishedAt.IsZero() && !ended[key] {
			t.Gaps = append(t.Gaps, "missing call result: "+key)
		}
	}
	t.TruncatedTail = t.TruncatedTail || result.TruncatedTail
	return err
}

// CallID joins query records using framework identity even before timeline flush.
// Stage identity is a fallback for projections constructed directly by callers.
func CallID(record map[string]any) string {
	if id := textField(record, "call_id"); id != "" {
		return id
	}
	return textField(record, "stage_id")
}
