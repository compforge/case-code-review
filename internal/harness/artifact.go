package harness

import (
	"context"
	"sort"

	"github.com/compforge/agentgo"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
)

// A continuation carries values, so the next bare loop still creates and owns
// an independent manager. IDs identify immutable source observations.
func mergeArtifacts(groups ...[]agentgo.Artifact) []agentgo.Artifact {
	values := make(map[string]agentgo.Artifact)
	for _, group := range groups {
		for _, value := range group {
			values[value.ID()] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]agentgo.Artifact, 0, len(keys))
	for _, key := range keys {
		out = append(out, values[key])
	}
	return out
}

// Register the final result after all tool handling and reuse decisions, before
// it enters the transcript. Decode once and hand the same typed message to the
// result factory, preserving one call/result pair even for batch reads.
func (e *Execution) artifactMiddleware() agentgo.ToolMiddleware {
	return func(ctx context.Context, execution agentgo.ToolExecution, next agentgo.ToolExecuteFunc) (agentgo.ToolResult, error) {
		result, err := next(ctx, execution)
		if err != nil {
			return result, err
		}
		message := decodeToolMessage(execution.Call, result)
		if err := msg.RegisterArtifacts(execution.Artifacts, []agentgo.AgentMessage{message}); err != nil {
			return result, err
		}
		result.Details = message
		return result, nil
	}
}
