package main

// ccr export projects native AgentGo Sessions into ATIF. A Review links R1
// Sessions and R2 Lane Sessions through JobView; it is not an agent Session.

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/qiankunli/case-code-review/internal/recording"
	"github.com/qiankunli/case-code-review/internal/recording/operation"
	"github.com/qiankunli/case-code-review/internal/runner"
)

const atifSchemaVersion = "ATIF-v1.8"

// ── ATIF shapes (field names per the RFC; omitempty keeps output lean) ────────

type atifTrajectory struct {
	SchemaVersion string         `json:"schema_version"`
	SessionID     string         `json:"session_id,omitempty"`
	TrajectoryID  string         `json:"trajectory_id,omitempty"`
	Agent         atifAgent      `json:"agent"`
	Steps         []*atifStep    `json:"steps"`
	FinalMetrics  *atifFinal     `json:"final_metrics,omitempty"`
	Extra         map[string]any `json:"extra,omitempty"`
}

type atifAgent struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	ModelName string `json:"model_name,omitempty"`
}

type atifStep struct {
	StepID           int            `json:"step_id"`
	Timestamp        string         `json:"timestamp,omitempty"`
	Source           string         `json:"source"` // system | user | agent
	Message          any            `json:"message"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ModelName        string         `json:"model_name,omitempty"`
	ToolCalls        []atifToolCall `json:"tool_calls,omitempty"`
	Observation      *atifObs       `json:"observation,omitempty"`
	Metrics          *atifMetrics   `json:"metrics,omitempty"`
	LLMCallCount     *int           `json:"llm_call_count,omitempty"`
	IsCopiedContext  bool           `json:"is_copied_context,omitempty"`
	Extra            map[string]any `json:"extra,omitempty"`
}

type atifToolCall struct {
	ToolCallID   string         `json:"tool_call_id"`
	FunctionName string         `json:"function_name"`
	Arguments    map[string]any `json:"arguments"`
}

type atifObs struct {
	Results []atifObsResult `json:"results"`
}

type atifObsResult struct {
	SourceCallID string         `json:"source_call_id,omitempty"`
	Content      any            `json:"content"`
	Extra        map[string]any `json:"extra,omitempty"`
}

type atifMetrics struct {
	PromptTokens     int            `json:"prompt_tokens"`
	CompletionTokens int            `json:"completion_tokens"`
	CachedTokens     int            `json:"cached_tokens"`
	Extra            map[string]any `json:"extra,omitempty"`
}

type atifFinal struct {
	TotalPromptTokens     int `json:"total_prompt_tokens,omitempty"`
	TotalCompletionTokens int `json:"total_completion_tokens,omitempty"`
	TotalCachedTokens     int `json:"total_cached_tokens,omitempty"`
	TotalSteps            int `json:"total_steps,omitempty"`
}

// exportEvent is the read-side of session records for export — richer than
// statsEvent (full messages / tool calls / usage), still decode-what-we-need.
type exportEvent struct {
	CallID                 string           `json:"call_id"`
	SessionRef             map[string]any   `json:"session_ref"`
	OperationID            string           `json:"operation_id"`
	RequestID              string           `json:"request_id"`
	ToolCallID             string           `json:"tool_call_id"`
	ReviewID               string           `json:"review_id"`
	Type                   string           `json:"type"`
	Timestamp              string           `json:"timestamp"`
	SessionID              string           `json:"sessionId"`
	Model                  string           `json:"model"`
	Cwd                    string           `json:"cwd"`
	GitBranch              string           `json:"gitBranch"`
	ReviewMode             string           `json:"reviewMode"`
	DiffFrom               string           `json:"diffFrom"`
	DiffTo                 string           `json:"diffTo"`
	ToolVersion            string           `json:"tool_version"`
	Features               map[string]bool  `json:"features"`
	Params                 map[string]any   `json:"params"`
	GitHead                string           `json:"git_head"`
	EvalTag                string           `json:"eval_tag"`
	BizID                  string           `json:"biz_id"`
	ArtifactKind           string           `json:"artifact_kind"`
	Data                   map[string]any   `json:"data"`
	ScopeID                string           `json:"scope_id"`
	FilePath               string           `json:"filePath"`
	Kind                   string           `json:"kind"`
	Paths                  []string         `json:"paths"`
	TaskType               string           `json:"taskType"`
	ExecutionID            string           `json:"execution_id"`
	Outcome                string           `json:"outcome"`
	Reason                 string           `json:"reason"`
	Turns                  int              `json:"turns"`
	ProjectionNo           int              `json:"projection_no"`
	Metadata               map[string]any   `json:"metadata"`
	Items                  []map[string]any `json:"items"`
	InitialOutlineAttempts []map[string]any `json:"initial_outline_attempts"`
	RequestNo              int              `json:"request_no"`
	Messages               []exportMessage  `json:"messages"`
	Tools                  json.RawMessage  `json:"tools"`
	ModelCall              json.RawMessage  `json:"model_call"`
	Content                string           `json:"content"`
	ContentParts           json.RawMessage  `json:"content_parts"`
	UsageSource            string           `json:"usage_source"`
	Reasoning              string           `json:"reasoning"`
	ToolCalls              json.RawMessage  `json:"tool_calls"`
	DurationMS             float64          `json:"duration_ms"`
	Usage                  *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		CacheReadTokens  int `json:"cache_read_tokens"`
	} `json:"usage"`
	ToolName string         `json:"tool_name"`
	Args     string         `json:"arguments"`
	Result   string         `json:"result"`
	OK       *bool          `json:"ok"`
	Error    string         `json:"error"`
	Failure  map[string]any `json:"failure"`
}

type exportMessage struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Timestamp string          `json:"timestamp,omitempty"`
}

func runExport(args []string) error {
	format := "atif"
	unitID := ""
	pretty := false
	var paths []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--format":
			if i+1 < len(args) {
				format = args[i+1]
				i++
			}
		case "--unit":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return fmt.Errorf("--unit requires a Unit ID")
			}
			unitID = args[i+1]
			i++
		case "--pretty":
			pretty = true
		case "-h", "--help":
			fmt.Println(`Usage: ccr export [recording.jsonl | dir]... [--format atif|job] [--unit ID] [--pretty]

Convert session transcripts to ATIF (Agent Trajectory Interchange Format,
Harbor RFC 0001): one trajectory per native agent Session. R1 Sessions and
R2 Lane Sessions are separate documents; a Lane includes its consecutive loops.
Output is JSONL on stdout (one trajectory per Session; --pretty indents). With no path, exports the most recent recording.
Use --format job --unit ID to export the recorded Unit JobView across R1,
R2 and Trial, with native model requests and evidence gaps. This is offline
projection only; it does not rerun the review or judge its correctness.`)
			return nil
		default:
			paths = append(paths, args[i])
		}
	}
	if format != "atif" && format != "job" {
		return fmt.Errorf("unsupported export format %q (supported: atif, job)", format)
	}
	if (format == "job") != (unitID != "") {
		return fmt.Errorf("--format job requires --unit ID; --unit is only valid for job export")
	}

	files, err := resolveSessionFiles(paths)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no session .jsonl files found")
	}
	enc := json.NewEncoder(os.Stdout)
	if pretty {
		enc.SetIndent("", "  ")
	}
	for _, f := range files {
		if format == "job" {
			view, err := recording.ReadJob(f, unitID, runner.SessionCodecOptions()...)
			if err != nil {
				return fmt.Errorf("export Unit %s from %s: %w", unitID, f, err)
			}
			if err := enc.Encode(view); err != nil {
				return err
			}
			continue
		}
		trajectories, err := exportSession(f)
		if err != nil {
			return fmt.Errorf("export %s: %w", f, err)
		}
		for _, trajectory := range trajectories {
			if err := enc.Encode(trajectory); err != nil {
				return err
			}
		}
	}
	return nil
}

func exportSession(path string) ([]atifTrajectory, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	root := &atifTrajectory{
		SchemaVersion: atifSchemaVersion,
		Agent:         atifAgent{Name: "case-code-review", Version: "unknown"},
		Steps:         []*atifStep{},
	}
	sessionByExecution := map[string]string{}
	// +spec=One native Session forms one Trajectory; execution boundaries remain metadata within a Lane
	// +link=repo://eval/trajectory/README.md
	type chain struct {
		steps         []*atifStep
		requests      map[string]*atifStep
		prompts       map[string]exportEvent
		extra         map[string]any
		total         atifFinal
		stepID        int
		sawFirst      bool
		contextCounts map[string]int
		sessionID     string
		executions    []map[string]any
		sawProjection bool
	}
	chains := map[string]*chain{}
	var order []string
	debriefs := map[string]exportEvent{}

	get := func(e exportEvent) *chain {
		id := e.ExecutionID
		if id == "" {
			id = e.ScopeID
		}
		sessionID, _ := e.SessionRef["session_id"].(string)
		if sessionID == "" {
			sessionID = sessionByExecution[e.ExecutionID]
		}
		if sessionID != "" {
			id = sessionID
		}
		c := chains[id]
		if c == nil {
			c = &chain{sessionID: sessionID, steps: []*atifStep{}, contextCounts: map[string]int{}, extra: map[string]any{"scope_id": e.ScopeID}, requests: map[string]*atifStep{}, prompts: map[string]exportEvent{}}
			if e.FilePath != "" {
				c.extra["file_path"] = e.FilePath
			}
			if e.Kind != "" {
				c.extra["scope_kind"] = e.Kind
			}
			if len(e.Paths) > 0 {
				c.extra["paths"] = e.Paths
			}
			chains[id] = c
			order = append(order, id)
		}
		return c
	}

	transcript, err := recording.ReadReview(f.Name())
	if err != nil {
		return nil, err
	}
	stages := transcript.OperationIndex()
	for _, stage := range stages {
		var executionID, sessionID string
		_ = json.Unmarshal(stage.Attributes["execution_id"], &executionID)
		_ = json.Unmarshal(stage.Attributes["session_id"], &sessionID)
		if executionID != "" && sessionID != "" {
			sessionByExecution[executionID] = sessionID
		}
	}

	for _, record := range transcript.Records {
		raw, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		var e exportEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		if stage, ok := stages[operation.ID(e.OperationID)]; ok {
			e.Timestamp = stage.StartedAt.Format(time.RFC3339Nano)
			if !stage.FinishedAt.IsZero() {
				e.DurationMS = float64(stage.Duration(time.Time{}).Milliseconds())
			}
		}
		switch e.Type {
		case "session_start":
			root.SessionID = e.SessionID
			root.Agent.ModelName = e.Model
			root.Extra = map[string]any{
				"review_id": e.SessionID, "repo": e.Cwd, "branch": e.GitBranch, "review_mode": e.ReviewMode,
				"diff_from": e.DiffFrom, "diff_to": e.DiffTo,
			}
			if e.ToolVersion != "" {
				root.Agent.Version = e.ToolVersion
				root.Extra["tool_version"] = e.ToolVersion
			}
			if len(e.Features) > 0 {
				root.Extra["features"] = e.Features
			}
			if len(e.Params) > 0 {
				root.Extra["params"] = e.Params
			}
			if e.GitHead != "" {
				root.Extra["git_head"] = e.GitHead
			}
			if e.EvalTag != "" {
				root.Extra["eval_tag"] = e.EvalTag
			}
			if e.BizID != "" {
				root.Extra["biz_id"] = e.BizID
			}
		case "context_projected":
			c := get(e)
			projections, _ := c.extra["context_projections"].([]map[string]any)
			c.extra["context_projections"] = append(projections, map[string]any{
				"projection_no": e.ProjectionNo, "execution_id": e.ExecutionID,
				"items": e.Items,
			})
			if !c.sawProjection {
				c.sawProjection = true
				c.extra["initial_context"] = e.Items
			}
		case "llm_request":
			c := get(e)
			requestID := e.CallID
			if requestID == "" {
				requestID = e.OperationID
			}
			c.prompts[requestID] = e
			// +spec=Repeated prompts do not duplicate trajectory steps; newly appended user messages remain visible
			occurrences := map[string]int{}
			for _, m := range e.Messages {
				raw, _ := json.Marshal(m)
				key := string(raw)
				occurrences[key]++
				if occurrences[key] <= c.contextCounts[key] {
					continue
				}
				c.contextCounts[key] = occurrences[key]
				if c.sawFirst && m.Role != "system" && m.Role != "user" {
					continue
				}
				c.stepID++
				message, issues := rawToMessage(m.Content)
				extra := map[string]any{"execution_id": e.ExecutionID, "request_message": m}
				if len(issues) > 0 {
					extra["content_projection_issues"] = issues
				}
				c.steps = append(c.steps, &atifStep{StepID: c.stepID, Timestamp: e.Timestamp,
					Source: atifSource(m.Role), Message: message,
					IsCopiedContext: m.Role == "assistant" || m.Role == "tool", LLMCallCount: intPointer(0),
					Extra: extra,
				})
			}
			c.sawFirst = true

		case "llm_response", "llm_error":
			c := get(e)
			c.stepID++
			st := &atifStep{
				StepID: c.stepID, Timestamp: e.Timestamp, Source: "agent",
				Message: e.Content, ReasoningContent: e.Reasoning,
				ModelName: e.Model, LLMCallCount: intPointer(1),
				Extra: map[string]any{"task_type": e.TaskType, "execution_id": e.ExecutionID, "operation_id": e.OperationID, "session_ref": e.SessionRef},
			}

			if len(e.ContentParts) > 0 {
				var issues []string
				st.Message, issues = rawToMessage(e.ContentParts)
				if len(issues) > 0 {
					st.Extra["content_projection_issues"] = issues
				}
			}
			if e.Type == "llm_error" {
				st.Extra["llm_error"] = e.Error
				st.Extra["partial"] = true
			}
			if len(e.Failure) > 0 {
				st.Extra["failure"] = e.Failure
			}
			if e.UsageSource == "missing" {
				st.Extra["usage_source"] = "missing"
			}
			if stage, ok := stages[operation.ID(e.OperationID)]; ok && !stage.FinishedAt.IsZero() {
				st.Extra["duration_ms"] = e.DurationMS
			}
			if e.Usage != nil {
				st.Metrics = &atifMetrics{PromptTokens: e.Usage.PromptTokens, CompletionTokens: e.Usage.CompletionTokens, CachedTokens: e.Usage.CacheReadTokens}
				c.total.TotalPromptTokens += st.Metrics.PromptTokens
				c.total.TotalCompletionTokens += st.Metrics.CompletionTokens
				c.total.TotalCachedTokens += st.Metrics.CachedTokens
			}

			var toolCalls []map[string]any
			_ = json.Unmarshal(e.ToolCalls, &toolCalls)
			for _, tc := range toolCalls {
				id, name, argsObj := parseRawToolCall(tc)
				st.ToolCalls = append(st.ToolCalls, atifToolCall{
					ToolCallID: id, FunctionName: name, Arguments: argsObj,
				})
			}
			c.steps = append(c.steps, st)
			id := e.CallID
			if id == "" {
				id = e.OperationID
			}
			c.requests[id] = st
			if request, ok := c.prompts[id]; ok {
				st.Extra["request_messages"] = request.Messages
				if len(request.Tools) > 0 {
					st.Extra["request_tools"] = request.Tools
				}
				if len(request.ModelCall) > 0 {
					st.Extra["request_model_call"] = request.ModelCall
				}
			}
			if len(e.ContentParts) > 0 {
				st.Extra["response_content"] = e.ContentParts
			}
		case "tool_result":
			c := get(e)
			owner := c.requests[e.RequestID]
			matched := false
			if owner != nil {
				for _, call := range owner.ToolCalls {
					if call.ToolCallID == e.ToolCallID {
						matched = true
					}
				}
			}
			if owner == nil || !matched {
				// Preserve unassigned outcomes without inventing a model or tool call.
				c.stepID++
				owner = &atifStep{StepID: c.stepID, Source: "agent", Message: "", LLMCallCount: intPointer(0), Extra: map[string]any{"unassigned_observation": true, "execution_id": e.ExecutionID}}
				c.steps = append(c.steps, owner)
			}
			res := atifObsResult{
				Content: e.Result,
				Extra:   map[string]any{"tool_name": e.ToolName, "arguments": e.Args},
			}
			if e.OK != nil {
				res.Extra["ok"] = *e.OK
			}
			for key, value := range e.Metadata {
				res.Extra[key] = value
			}
			res.SourceCallID = e.ToolCallID
			res.Extra["operation_id"] = e.OperationID
			res.Extra["session_ref"] = e.SessionRef
			res.Extra["execution_id"] = e.ExecutionID
			if stage, ok := stages[operation.ID(e.OperationID)]; ok && !stage.FinishedAt.IsZero() {
				res.Extra["duration_ms"] = e.DurationMS
			}
			if owner.Observation == nil {
				owner.Observation = &atifObs{}
			}
			owner.Observation.Results = append(owner.Observation.Results, res)

		case "debrief":
			debriefs[e.ScopeID] = e
		}
	}

	if root.Extra == nil {
		root.Extra = map[string]any{}
	}
	if gaps := transcript.IncompleteReasons(); len(gaps) > 0 {
		root.Extra["recording_incomplete"] = strings.Join(gaps, "; ")
	}
	for _, fact := range transcript.ExecutionFacts() {
		raw, _ := json.Marshal(fact)
		var e exportEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		c := get(e)
		execution := map[string]any{"execution_id": e.ExecutionID, "outcome": e.Outcome, "reason": e.Reason, "turns": e.Turns}
		if duration, ok := fact["duration_ms"]; ok {
			execution["duration_ms"] = duration
		}
		c.executions = append(c.executions, execution)

	}

	trajectories := make([]atifTrajectory, 0, len(order))
	for _, id := range order {
		c := chains[id]
		if debrief, ok := debriefs[c.extra["scope_id"].(string)]; ok && len(debrief.InitialOutlineAttempts) > 0 {
			c.extra["initial_outline_attempts"] = debrief.InitialOutlineAttempts
		}
		c.total.TotalSteps = len(c.steps)
		extra := maps.Clone(root.Extra)
		maps.Copy(extra, c.extra)
		extra["executions"] = c.executions
		// A request without a terminal event is evidence, not a completed LLM step.
		pending := []map[string]any{}
		for _, requestID := range slices.Sorted(maps.Keys(c.prompts)) {
			if c.requests[requestID] != nil {
				continue
			}
			request := c.prompts[requestID]
			pending = append(pending, map[string]any{"request_id": requestID, "execution_id": request.ExecutionID, "session_ref": request.SessionRef, "timestamp": request.Timestamp, "messages": request.Messages})
		}
		if len(pending) > 0 {
			extra["pending_requests"] = pending
		}

		if c.sessionID == "" {
			extra["session_identity_missing"] = true
		}

		sub := atifTrajectory{
			SchemaVersion: atifSchemaVersion,
			TrajectoryID:  id,
			SessionID:     c.sessionID,
			Agent:         root.Agent,
			Steps:         c.steps,
			FinalMetrics:  &c.total,
			Extra:         extra,
		}
		trajectories = append(trajectories, sub)
	}
	return trajectories, nil
}

// atifSource maps an OpenAI-style role onto ATIF's step source vocabulary.
func atifSource(role string) string {
	switch role {
	case "system", "user":
		return role
	default: // assistant / tool / anything model-side
		return "agent"
	}
}

// rawToMessage maps native content blocks to ATIF content. Reasoning and calls
// have their own standard fields; the full request/response remains in extra.
func rawToMessage(raw json.RawMessage) (any, []string) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var blocks []map[string]any
	if json.Unmarshal(raw, &blocks) != nil {
		return "", []string{"content is not a string or content block list"}
	}
	parts := []map[string]any{}
	var issues []string
	for _, block := range blocks {
		switch block["type"] {
		case "text":
			text, _ := block["text"].(string)
			parts = append(parts, map[string]any{"type": "text", "text": text})
		case "image":
			if source, ok := block["source"].(map[string]any); ok {
				parts = append(parts, map[string]any{"type": "image", "source": source})
				continue
			}
			image, _ := block["image"].(map[string]any)
			media, _ := image["mime_type"].(string)
			path, _ := image["url"].(string)
			if data, _ := image["data"].(string); data != "" {
				path = "data:" + media + ";base64," + data
			}
			if media != "" && path != "" {
				parts = append(parts, map[string]any{"type": "image", "source": map[string]any{"media_type": media, "path": path}})
			} else {
				issues = append(issues, "image MIME type or path is missing")
			}
		case "audio":
			if source, ok := block["source"].(map[string]any); ok {
				parts = append(parts, map[string]any{"type": "audio", "source": source})
			} else {
				issues = append(issues, "audio source is missing")
			}
		case "thinking", "toolCall": // Represented separately or retained as copied context.
		default:
			issues = append(issues, fmt.Sprintf("unmapped content type %v", block["type"]))
		}
	}
	if len(parts) == 0 {
		return "", issues
	}
	return parts, issues
}

func intPointer(value int) *int { return &value }

// parseRawToolCall unwraps one recorded tool call into ATIF fields. Sessions
// record the flat shape {id, name, arguments}; the OpenAI-nested shape
// ({id, function:{name, arguments}}) is accepted too. Unparseable arguments
// survive under "raw" instead of being dropped.
func parseRawToolCall(tc map[string]any) (id, name string, args map[string]any) {
	id, _ = tc["id"].(string)
	name, _ = tc["name"].(string)
	rawArgs, _ := tc["arguments"].(string)
	if fn, ok := tc["function"].(map[string]any); ok {
		if n, ok := fn["name"].(string); ok && n != "" {
			name = n
		}
		if s, ok := fn["arguments"].(string); ok && s != "" {
			rawArgs = s
		}
	}
	if rawArgs != "" {
		if err := json.Unmarshal([]byte(rawArgs), &args); err != nil || args == nil {
			args = map[string]any{"raw": rawArgs}
		}
	}
	if args == nil {
		args = map[string]any{}
	}
	return id, name, args
}
