package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/recording"
)

// recordModel applies CCR call policy inside the shared AgentGo executor.
// Provider streaming, request snapshots and execution lifecycle belong to AgentGo.
func (e *Execution) recordModel() agentgo.ModelMiddleware {
	return func(ctx context.Context, call agentgo.ModelExecution, next agentgo.ModelExecuteFunc) (agentgo.ModelResult, error) {
		conversation := call.ParentID == ""
		taskType := e.spec.TaskType
		if !conversation {
			taskType = recording.MemoryCompressionTask
		}
		if call.Request.Config.MaxTokens == 0 {
			call.Request.Config.MaxTokens = e.spec.MaxTokens
		}
		record := e.recorder.beginModel(taskType, call.Request.Messages)
		started := time.Now()
		result, err := next(ctx, call)
		response := &agentgo.LLMResponse{Message: result.Message}
		duration := time.Since(started)
		if record != nil {
			record.SetResult(response, err, duration)
		}
		e.recorder.finishModel(response, duration, conversation, err == nil)
		if conversation {
			e.turns.observeUsage(result.Message.Usage)
		}
		if err != nil {
			return result, err
		}
		result.Message, err = normalizeResponse(response, call.Request.Tools)
		return result, err
	}
}

// normalizeResponse preserves provider content and repairs only lossless tool argument drift.
func normalizeResponse(response *agentgo.LLMResponse, tools []agentgo.ToolSpec) (agentgo.Message, error) {
	if response == nil {
		return agentgo.Message{}, fmt.Errorf("harness: model returned no response")
	}
	message := response.Message
	message.Content = append([]agentgo.ContentBlock(nil), message.Content...)
	for i := range message.Content {
		b := &message.Content[i]
		if b.ToolCall != nil {
			c := *b.ToolCall
			c.Args = canonicalToolArguments(c.Name, c.Args, toolParameters(tools, c.Name))
			b.ToolCall = &c
		}
	}
	return message, nil
}

// canonicalToolArguments repairs only lossless schema drift before AgentGo
// validates the call. Empty, malformed, or semantically incomplete arguments
// remain errors for the model to correct on its next turn.
func canonicalToolArguments(name string, raw json.RawMessage, parameters map[string]any) json.RawMessage {
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil {
		return raw
	}
	unwrapToolArgumentObject(args, parameters)

	switch name {
	case tool.FileRead.Name(), tool.FileReadBase.Name():
		canonicalizeReadArguments(args)
	case tool.CodeSearch.Name():
		canonicalizeSearchArguments(args)
	}

	canonical, err := json.Marshal(args)
	if err != nil {
		return raw
	}
	return canonical
}

// unwrapToolArgumentObject removes one redundant model-produced wrapper when
// the nested object visibly matches the tool's declared root properties. The
// schema, rather than a domain tool-name list, owns the decision so Harness
// remains independent of domain result tools. Unknown, empty, and ambiguous
// wrappers stay untouched and fail normal validation.
func unwrapToolArgumentObject(args, parameters map[string]any) {
	if len(args) != 1 || len(parameters) == 0 {
		return
	}
	properties, _ := parameters["properties"].(map[string]any)
	if len(properties) == 0 {
		return
	}
	for wrapper, value := range args {
		if _, declared := properties[wrapper]; declared {
			return
		}
		nested, ok := value.(map[string]any)
		if !ok || len(nested) == 0 || !sharesDeclaredProperty(nested, properties) {
			return
		}
		clear(args)
		maps.Copy(args, nested)
	}
}

func sharesDeclaredProperty(args, properties map[string]any) bool {
	for name := range args {
		if _, ok := properties[name]; ok {
			return true
		}
	}
	return false
}

func toolParameters(tools []agentgo.ToolSpec, name string) map[string]any {
	for _, spec := range tools {
		if spec.Name == name {
			parameters, _ := spec.Parameters.(map[string]any)
			return parameters
		}
	}
	return nil
}

func canonicalizeReadArguments(args map[string]any) {
	if reads, ok := args["reads"]; ok {
		switch reads := reads.(type) {
		case map[string]any:
			args["reads"] = []any{reads}
		}
		return
	}
	if _, ok := args["file_path"]; ok {
		item := maps.Clone(args)
		clear(args)
		args["reads"] = []any{item}
	}
}
func canonicalizeSearchArguments(args map[string]any) {
	if searches, ok := args["searches"]; ok {
		switch searches := searches.(type) {
		case map[string]any:
			canonicalizeSearchItem(searches)
			args["searches"] = []any{searches}
		case []any:
			for _, value := range searches {
				if item, ok := value.(map[string]any); ok {
					canonicalizeSearchItem(item)
				}
			}
		}
		return
	}

	if queries, ok := args["queries"].([]any); ok && len(queries) > 0 {
		shared := searchSharedArguments(args)
		searches := make([]any, 0, len(queries))
		for _, value := range queries {
			switch value := value.(type) {
			case string:
				item := maps.Clone(shared)
				item["query"] = value
				canonicalizeSearchItem(item)
				searches = append(searches, item)
			case map[string]any:
				item := maps.Clone(shared)
				maps.Copy(item, value)
				canonicalizeSearchItem(item)
				searches = append(searches, item)
			default:
				return
			}
		}
		clear(args)
		args["searches"] = searches
		return
	}

	canonicalizeSearchItem(args)
	if _, ok := args["query"]; ok {
		item := maps.Clone(args)
		clear(args)
		args["searches"] = []any{item}
	}
}

func canonicalizeSearchItem(item map[string]any) {
	if pattern, ok := item["file_patterns"].(string); ok && strings.TrimSpace(pattern) != "" {
		item["file_patterns"] = []any{pattern}
	}

	if _, exists := item["syntax"]; !exists {
		if useRegexp, ok := item["use_perl_regexp"].(bool); ok {
			if useRegexp {
				item["syntax"] = "regexp"
			} else {
				item["syntax"] = "literal"
			}
		}
	}
	if syntax, ok := item["syntax"].(string); ok {
		switch strings.ToLower(strings.TrimSpace(syntax)) {
		case "regex", "regexp", "perl":
			item["syntax"] = "regexp"
		case "literal":
			item["syntax"] = "literal"
		}
	}
	delete(item, "use_perl_regexp")

	if _, exists := item["query"]; !exists {
		if pattern, ok := item["pattern"].(string); ok && safePatternAlias(pattern, item["syntax"]) {
			item["query"] = pattern
			delete(item, "pattern")
		}
	}
	delete(item, "contextAround")
	delete(item, "outputMode")
	delete(item, "context_lines")
	delete(item, "symbol_context")
	delete(item, "purpose")
}

func safePatternAlias(pattern string, syntax any) bool {
	value, _ := syntax.(string)
	return !strings.EqualFold(strings.TrimSpace(value), "literal") || !strings.Contains(pattern, `\`)
}

func searchSharedArguments(args map[string]any) map[string]any {
	shared := make(map[string]any, 4)
	for _, key := range []string{"file_patterns", "case_sensitive", "use_perl_regexp", "syntax"} {
		if value, ok := args[key]; ok {
			shared[key] = value
		}
	}
	return shared
}

func metadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return value
}
