package viewer

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/compforge/agentgo"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/recording"
	"github.com/qiankunli/case-code-review/internal/recording/operation"
)

// Stage views are Viewer projections. Recording producers never select UI tabs.
type stageView struct{ ID, Label string }

func (row timelineRow) Views() []stageView {
	views := []stageView{{"default", "Default"}}
	switch row.Name {
	case "model.attempt", "llm.request":
		views = append(views, stageView{"model", "Model"})
	case "execution":
		views = append(views, stageView{"agent", "Agent"})
	}
	return views
}

type stageSource struct {
	rows []operation.Record
	byID map[operation.ID]operation.Record
}

func readStageSource(path string) (*stageSource, error) {
	if _, err := readSessionStart(path); err != nil {
		return nil, err
	}
	transcript, err := recording.ReadJournal(path)
	if err != nil {
		return nil, err
	}
	source := &stageSource{rows: transcript.Journal.Operations, byID: transcript.OperationIndex()}
	for _, stage := range source.rows {
		source.byID[stage.ID] = stage
	}
	return source, nil
}

func (s *stageSource) sessionFile(stage operation.Record) (string, error) {
	seen := map[operation.ID]bool{}
	for parent := stage; parent.ID != "" && !seen[parent.ID]; parent = s.byID[parent.ParentID] {
		seen[parent.ID] = true
		name, _ := operation.AttributeValue[string](parent.Attributes, "session_file")
		if name == "" {
			continue
		}
		if filepath.Base(name) != name || !strings.HasSuffix(name, ".jsonl") {
			return "", fmt.Errorf("invalid Session filename")
		}
		return name, nil
	}
	return "", nil
}

// Native failed messages remain inspection evidence even when replay excludes them.
func recordedModelMessage(raw json.RawMessage) (*agentgo.Message, error) {
	codec, err := msg.Codec()
	if err != nil {
		return nil, err
	}
	var message agentgo.AgentMessage
	if err := codec.Unmarshal(raw, &message); err != nil {
		return nil, err
	}
	response, ok := message.(agentgo.Message)
	if !ok {
		response, ok = message.ToMessage()
	}
	if !ok {
		return nil, fmt.Errorf("model response cannot be projected as a message")
	}
	return &response, nil
}
