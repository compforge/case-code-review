package main

// `ccr export` — convert session JSONL transcripts into ATIF (Agent Trajectory
// Interchange Format, Harbor RFC 0001), the interchange format SFT/RL/eval and
// trajectory-mining tooling consume. One review session becomes one root
// Trajectory; each unit's sequential chain becomes a subagent trajectory, so the
// per-unit review loops stay separately minable. The session JSONL remains the
// source of truth — this is a lossless-enough projection, with ccr-specific
// leftovers (durations, tool ok flags, task types) tucked into `extra`.

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/compforge/go-stdx/timeline"
	"github.com/qiankunli/case-code-review/internal/harness/session"
)

const atifSchemaVersion = "ATIF-v1.8"

// ── ATIF shapes (field names per the RFC; omitempty keeps output lean) ────────

type atifTrajectory struct {
	SchemaVersion string           `json:"schema_version"`
	SessionID     string           `json:"session_id,omitempty"`
	TrajectoryID  string           `json:"trajectory_id,omitempty"`
	Agent         atifAgent        `json:"agent"`
	Steps         []*atifStep      `json:"steps"`
	FinalMetrics  *atifFinal       `json:"final_metrics,omitempty"`
	Extra         map[string]any   `json:"extra,omitempty"`
	Subagents     []atifTrajectory `json:"subagent_trajectories,omitempty"`
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
	LLMCallCount     int            `json:"llm_call_count,omitempty"`
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
	Content      string         `json:"content"`
	Extra        map[string]any `json:"extra,omitempty"`
}

type atifMetrics struct {
	PromptTokens     int            `json:"prompt_tokens,omitempty"`
	CompletionTokens int            `json:"completion_tokens,omitempty"`
	CachedTokens     int            `json:"cached_tokens,omitempty"`
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
	StageID                string           `json:"stage_id"`
	RequestID              string           `json:"request_id"`
	ToolCallID             string           `json:"tool_call_id"`
	TimelineID             string           `json:"timeline_id"`
	TimelineUpdate         timeline.Update  `json:"update"`
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
	Content                string           `json:"content"`
	Reasoning              string           `json:"reasoning"`
	ToolCalls              json.RawMessage  `json:"tool_calls"`
	DurationMS             float64          `json:"duration_ms"`
	Usage                  struct {
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
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func runExport(args []string) error {
	format := "atif"
	pretty := false
	var paths []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--format":
			if i+1 < len(args) {
				format = args[i+1]
				i++
			}
		case "--pretty":
			pretty = true
		case "-h", "--help":
			fmt.Println(`Usage: ccr export [session.jsonl | dir]... [--format atif] [--pretty]

Convert session transcripts to ATIF (Agent Trajectory Interchange Format,
Harbor RFC 0001): one root trajectory per session, one embedded subagent
trajectory per review unit. Output is JSONL on stdout (one trajectory per
session; --pretty indents). With no path, exports the most recent session.`)
			return nil
		default:
			paths = append(paths, args[i])
		}
	}
	if format != "atif" {
		return fmt.Errorf("unsupported export format %q (supported: atif)", format)
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
		traj, err := exportSession(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ccr export: skip %s: %v\n", f, err)
			continue
		}
		if err := enc.Encode(traj); err != nil {
			return err
		}
	}
	return nil
}

func exportSession(path string) (*atifTrajectory, error) {
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
	var reviewArtifacts []map[string]any
	// Each execution becomes a trajectory; auxiliary calls use their scope.
	// A Lane may run several independent loops, so its final loop cannot stand
	// in for the outcomes or context of earlier loops.
	type chain struct {
		steps         []*atifStep
		requests      map[string]*atifStep
		extra         map[string]any
		total         atifFinal
		stepID        int
		sawFirst      bool
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
		c := chains[id]
		if c == nil {
			c = &chain{extra: map[string]any{"scope_id": e.ScopeID}, requests: map[string]*atifStep{}}
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

	transcript, err := session.ReadTranscript(f)
	if err != nil {
		return nil, err
	}
	stages := transcript.StageIndex()
	for _, record := range transcript.Records {
		raw, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		var e exportEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		if stage, ok := stages[timeline.StageID(e.StageID)]; ok {
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
				"repo": e.Cwd, "branch": e.GitBranch, "review_mode": e.ReviewMode,
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
		case "artifact":
			reviewArtifacts = append(reviewArtifacts, map[string]any{
				"kind": e.ArtifactKind,
				"data": e.Data, "stage_id": e.StageID,
			})
		case "context_projected":
			c := get(e)
			projections, _ := c.extra["context_projections"].([]map[string]any)
			c.extra["context_projections"] = append(projections, map[string]any{
				"projection_no": e.ProjectionNo,
				"items":         e.Items,
			})
			if !c.sawProjection {
				c.sawProjection = true
				c.extra["initial_context"] = e.Items
			}
		case "llm_request":
			c := get(e)
			// Only the chain's FIRST request seeds steps: later requests replay the
			// same conversation cumulatively (prior messages + tool results already
			// captured as steps/observations here).
			if !c.sawFirst {
				c.sawFirst = true
				for _, m := range e.Messages {
					c.stepID++
					c.steps = append(c.steps, &atifStep{
						StepID: c.stepID, Timestamp: e.Timestamp,
						Source: atifSource(m.Role), Message: rawToMessage(m.Content),
					})
				}
			}
		case "llm_response":
			c := get(e)
			c.stepID++
			st := &atifStep{
				StepID: c.stepID, Timestamp: e.Timestamp, Source: "agent",
				Message: e.Content, ReasoningContent: e.Reasoning,
				ModelName: e.Model, LLMCallCount: 1,
				Metrics: &atifMetrics{
					PromptTokens:     e.Usage.PromptTokens,
					CompletionTokens: e.Usage.CompletionTokens,
					CachedTokens:     e.Usage.CacheReadTokens,
					Extra:            map[string]any{"duration_ms": e.DurationMS},
				},
				Extra: map[string]any{"task_type": e.TaskType, "stage_id": e.StageID},
			}

			if stage, ok := stages[timeline.StageID(e.StageID)]; !ok || stage.FinishedAt.IsZero() {
				delete(st.Metrics.Extra, "duration_ms")
			}
			var toolCalls []map[string]any
			_ = json.Unmarshal(e.ToolCalls, &toolCalls)
			for _, tc := range toolCalls {
				id, name, argsObj := parseRawToolCall(tc)
				st.ToolCalls = append(st.ToolCalls, atifToolCall{
					ToolCallID: id, FunctionName: name, Arguments: argsObj,
				})
			}
			c.total.TotalPromptTokens += e.Usage.PromptTokens
			c.total.TotalCompletionTokens += e.Usage.CompletionTokens
			c.total.TotalCachedTokens += e.Usage.CacheReadTokens
			c.steps = append(c.steps, st)
			c.requests[e.StageID] = st
		case "tool_result":
			c := get(e)
			owner := c.requests[e.RequestID]
			if owner == nil {
				continue // tool result with no owning agent step — drop, not invent
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
			res.Extra["stage_id"] = e.StageID
			if owner.Observation == nil {
				owner.Observation = &atifObs{}
			}
			owner.Observation.Results = append(owner.Observation.Results, res)

		case "llm_error":
			c := get(e)
			c.stepID++
			extra := map[string]any{"llm_error": e.Error, "stage_id": e.StageID}
			if stage, ok := stages[timeline.StageID(e.StageID)]; ok && !stage.FinishedAt.IsZero() {
				extra["duration_ms"] = e.DurationMS
			}
			if len(e.Failure) > 0 {
				extra["failure"] = e.Failure
			}
			c.steps = append(c.steps, &atifStep{
				StepID: c.stepID, Timestamp: e.Timestamp, Source: "agent", Message: "",
				Extra: extra,
			})
		case "debrief":
			debriefs[e.ScopeID] = e
		}
	}

	if root.Extra == nil {
		root.Extra = map[string]any{}
	}
	if transcript.Timeline.ID != "" {
		root.Extra["timeline"] = transcript.Timeline
	}
	if transcript.TruncatedTail {
		root.Extra["recording_incomplete"] = "truncated final record"
	}
	for _, fact := range transcript.ExecutionFacts() {
		raw, _ := json.Marshal(fact)
		var e exportEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		c := get(e)
		c.extra["execution_outcome"] = e.Outcome
		if e.ExecutionID != "" {
			c.extra["execution_id"] = e.ExecutionID
		}
		if e.Reason != "" {
			c.extra["execution_reason"] = e.Reason
		}
		if e.Turns > 0 {
			c.extra["execution_turns"] = e.Turns
		}
		if duration, ok := fact["duration_ms"]; ok {
			c.extra["execution_duration_ms"] = duration
		}
	}

	if len(reviewArtifacts) > 0 {
		if root.Extra == nil {
			root.Extra = map[string]any{}
		}
		root.Extra["review_artifacts"] = reviewArtifacts
	}

	rootFinal := atifFinal{}
	for _, id := range order {
		c := chains[id]
		if debrief, ok := debriefs[c.extra["scope_id"].(string)]; ok && len(debrief.InitialOutlineAttempts) > 0 {
			c.extra["initial_outline_attempts"] = debrief.InitialOutlineAttempts
		}
		c.total.TotalSteps = len(c.steps)
		sub := atifTrajectory{
			SchemaVersion: atifSchemaVersion,
			TrajectoryID:  id,
			SessionID:     root.SessionID,
			Agent:         root.Agent,
			Steps:         c.steps,
			FinalMetrics:  &c.total,
			Extra:         c.extra,
		}
		root.Subagents = append(root.Subagents, sub)
		rootFinal.TotalPromptTokens += c.total.TotalPromptTokens
		rootFinal.TotalCompletionTokens += c.total.TotalCompletionTokens
		rootFinal.TotalCachedTokens += c.total.TotalCachedTokens
		rootFinal.TotalSteps += c.total.TotalSteps
	}
	root.FinalMetrics = &rootFinal
	return root, nil
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

// rawToMessage keeps a plain string message as a string; anything richer
// (multimodal content parts) passes through as-is.
func rawToMessage(raw json.RawMessage) any {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var v any
	if err := json.Unmarshal(raw, &v); err == nil {
		return v
	}
	return string(raw)
}

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
