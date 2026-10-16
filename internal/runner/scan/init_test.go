package scan

import "github.com/qiankunli/case-code-review/internal/recording"

// Redirect session writes to test-sessions/ so this package's tests don't
// pollute the real sessions store.
func init() { recording.UseTestSessions() }
