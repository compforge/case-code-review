package session

// Redirect session writes to test-sessions/ so this package's tests don't
// pollute the real sessions store.
func init() { UseTestSessions() }
