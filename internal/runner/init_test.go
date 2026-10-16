package runner

import "github.com/qiankunli/case-code-review/internal/recording"

// Redirect session writes to test-sessions/ so agent tests (which construct
// Agents, and thus Review) don't pollute the real sessions store.
func init() { recording.UseTestSessions() }
