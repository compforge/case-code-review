package msg

import (
	"github.com/compforge/agentgo"
)

// Instruction holds a task's identity and constraints. Source evidence belongs
// in separate File/Diff messages so it remains independently compactable.
type Instruction struct{ agentgo.Message }

func (m Instruction) Raw() agentgo.AgentMessage                       { return m }
func (m Instruction) FixedContext() agentgo.AgentMessage              { return m }
func (m Instruction) Compact(float64) (agentgo.AgentMessage, float64) { return m, 1 }

func FixedText(role, content string) agentgo.AgentMessage {
	return Instruction{Message: Text(role, content).(agentgo.Message)}
}
