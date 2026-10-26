package viewer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/compforge/agentgo"
	agentsession "github.com/compforge/agentgo/session"
	"github.com/qiankunli/case-code-review/internal/console"
	"github.com/qiankunli/case-code-review/internal/recording/operation"
)

type stageCall struct {
	Kind          string                       `json:"kind"`
	Request       *agentgo.LLMRequest          `json:"request,omitempty"`
	Response      *agentgo.Message             `json:"response,omitempty"`
	InputRecorded bool                         `json:"input_recorded"`
	Ref           agentsession.Ref             `json:"ref"`
	Tool          string                       `json:"tool"`
	Input         json.RawMessage              `json:"input"`
	Output        *agentsession.ToolResultData `json:"output"`
	Error         string                       `json:"error,omitempty"`
	IsError       bool                         `json:"is_error"`
	Warnings      []string                     `json:"warnings,omitempty"`
}

// +spec=`Stage details load only the referenced local Session and match all four reference fields; missing call data remains missing, never borrowed from another run or attempt`
func loadStageCall(path string, id operation.ID) (*stageCall, error) {
	source, err := readStageSource(path)
	if err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(path, ".jsonl")
	stages := source.byID
	stage, ok := stages[id]
	if !ok {
		return nil, fmt.Errorf("call stage not found: %s", id)
	}
	call := &stageCall{}
	switch stage.Name {
	case "tool.invoke", "tool.execution":
		call.Kind = "tool"
	case "model.attempt", "llm.request":
		call.Kind = "model"
	default:
		return nil, fmt.Errorf("stage has no call content: %s", id)
	}
	call.Tool, _ = operation.AttributeValue[string](stage.Attributes, "tool")
	if json.Unmarshal(stage.Attributes["session_ref"], &call.Ref) != nil || call.Ref.SessionID == "" || call.Ref.RunID == "" || call.Ref.ExecutionID == "" || call.Ref.Attempt < 1 {
		call.Warnings = append(call.Warnings, "No Session reference recorded for this call.")
		return call, nil
	}
	name, err := source.sessionFile(stage)
	if err != nil {
		return nil, err
	}
	if name == "" {
		call.Warnings = append(call.Warnings, "No Session file recorded for this call.")
		return call, nil
	}
	file, err := os.Open(filepath.Join(base+".sessions", name))
	if err != nil {
		if os.IsNotExist(err) {
			call.Warnings = append(call.Warnings, "Referenced Session file is missing.")
			return call, nil
		}
		return nil, err
	}
	defer file.Close()
	result, err := agentsession.Read(file, func(record agentsession.Record) error {
		e := record.Execution
		if record.SessionID != call.Ref.SessionID || record.RunID != call.Ref.RunID || e == nil || e.ID != call.Ref.ExecutionID || e.Attempt != call.Ref.Attempt {
			return nil
		}
		var data agentsession.EventData
		if len(record.Data) == 0 {
			return nil
		}
		if err := json.Unmarshal(record.Data, &data); err != nil {
			return err
		}
		switch agentgo.EventType(record.Type) {
		case agentgo.EventModelRequest:
			call.Request = data.ModelRequest
		case agentgo.EventModelExecEnd:
			call.Error = data.Error
			if len(data.PartialMessage) > 0 {
				data.Message = data.PartialMessage
			}
			if len(data.Message) > 0 {
				response, err := recordedModelMessage(data.Message)
				if err != nil {
					return err
				}
				call.Response = response
			}
		case agentgo.EventToolExecStart:
			if stage.Name == "tool.execution" {
				call.Input = data.Args
				call.InputRecorded = len(data.Args) > 0
			}
		case agentgo.EventToolInvokeStart:
			call.Input = data.Args
			call.InputRecorded = len(data.Args) > 0
		case agentgo.EventToolExecEnd:
			// InvokeEnd carries timing/error only. The Session owns the final result
			// at ExecEnd; show that result explicitly, including rich content/details.
			call.Output, call.Error, call.IsError = data.ToolResult, data.Error, data.IsError
		case agentgo.EventToolInvokeEnd:
			if data.Error != "" {
				call.Error = data.Error
			}
			call.IsError = call.IsError || data.IsError
		}
		if data.Tool != "" {
			call.Tool = data.Tool
		}
		return nil
	})
	if err != nil {
		call.Warnings = append(call.Warnings, "Session read incomplete: "+err.Error())
	}
	if result.TruncatedTail {
		call.Warnings = append(call.Warnings, "Session has an incomplete final record.")
	}
	return call, nil
}

func handleStageCall(w http.ResponseWriter, r *http.Request, root, repo, sessionID string) {
	if repo == "" || sessionID == "" || strings.Contains(repo, "..") || strings.Contains(sessionID, "..") || strings.ContainsAny(repo+sessionID, `/\\`) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	call, err := loadStageCall(filepath.Join(root, repo, sessionID+".jsonl"), operation.ID(r.URL.Query().Get("stage")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(call); err != nil {
		fmt.Fprintf(console.Err(), "[viewer] stage call response: %v\n", err)
	}
}
