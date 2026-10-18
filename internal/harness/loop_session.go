package harness

import (
	"fmt"
	"sync"

	"github.com/compforge/agentgo"
	agentsession "github.com/compforge/agentgo/session"
)

// LoopSession owns the committed conversation and native recording across runs.
// Its zero value is ready to use; the owner closes it after all executions finish.
//
// +spec=`Sequential executions append messages to one Session; each execution binds fresh tools, budget and completion state`
// +why=`Conversation state outlives workers and must not retain a previous execution's callbacks`
// +link=component://docs/harness.md
type LoopSession struct {
	mu     sync.Mutex
	state  agentgo.AgentState
	local  *agentsession.Session
	path   string
	closed bool
}

// HasContext reports whether an earlier run committed conversation messages.
// Callers inspect it only while they own this session between executions.
func (s *LoopSession) HasContext() bool {
	return s != nil && len(s.state.Messages) != 0
}

func (s *LoopSession) acquire() error {
	if !s.mu.TryLock() {
		return fmt.Errorf("harness: loop session is already in use")
	}
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("harness: loop session is closed")
	}
	return nil
}
func (s *LoopSession) release() { s.mu.Unlock() }

func (s *LoopSession) retain(state agentgo.AgentState) {
	// Tools contain execution-bound closures. Only portable state survives a run.
	state.Tools = nil
	state.StreamMessage = nil
	state.PendingToolCalls = nil
	s.state = state
}

// Close releases the native Session after its last execution.
func (s *LoopSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.local != nil {
		return s.local.Close()
	}
	return nil
}
