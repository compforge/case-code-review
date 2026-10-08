package msg

import "github.com/compforge/agentgo"

// MaterialMessage lets caller-owned messages register reusable content and
// project references without exposing their domain types to Harness.
// WithMaterialReference must clone the message. An empty ref clears only the
// request-local reference, preserving any committed compaction.
type MaterialMessage interface {
	agentgo.AgentMessage
	MaterialArtifact() agentgo.Artifact
	MaterialReference() string
	MaterialVisible() bool
	WithMaterialReference(ref string) MaterialMessage
}

// TransformMaterials only reuses a body registered and visible in this request.
// Historical registration never licenses a dangling reference.
func TransformMaterials(input agentgo.TransformContext) []agentgo.AgentMessage {
	out := append([]agentgo.AgentMessage(nil), input.Messages...)
	seen := make(map[string]string)
	for i, message := range out {
		original, ok := message.(MaterialMessage)
		if !ok {
			continue
		}
		value := original.WithMaterialReference("")
		out[i] = value
		artifact := value.MaterialArtifact()
		if !value.MaterialVisible() || artifact == nil {
			continue
		}
		id := artifact.ID()
		if input.Artifacts != nil {
			if _, exists := input.Artifacts.GetArtifact(id); !exists {
				continue
			}
		}
		if ref, exists := seen[id]; exists {
			candidate := value.WithMaterialReference(ref)
			if len(candidate.TextContent()) < len(value.TextContent()) {
				out[i] = candidate
			}
		} else if ref := value.MaterialReference(); ref != "" {
			seen[id] = ref
		}
	}
	return out
}
