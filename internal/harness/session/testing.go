package session

// UseTestSessions redirects session persistence to the "test-sessions"
// subdirectory so test runs don't pollute the real ~/.casecodereview/sessions
// store with thousands of temp-repo dirs.
//
// Call it from init() in a _test.go file (or TestMain), before any test
// goroutines start. It is NOT safe for concurrent use.
func UseTestSessions() {
	sessionSubDir = "test-sessions"
}
