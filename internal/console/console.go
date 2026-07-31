package console

import (
	"io"
	"os"
	"sync"
)

var (
	w    io.Writer = os.Stdout
	errW io.Writer = os.Stderr
	mu   sync.RWMutex
)

// Out returns the current stdout writer (real stdout or discard).
func Out() io.Writer {
	mu.RLock()
	defer mu.RUnlock()
	return w
}

// Err returns the current stderr writer — real stderr, plus any sink added via
// AddErrSink. The runtime's `[ccr] WARNING/✘` diagnostics write here so a run
// can mirror them into a session-scoped log.
func Err() io.Writer {
	mu.RLock()
	defer mu.RUnlock()
	return errW
}

// AddErrSink tees stderr to sink (in addition to the original writer) and
// returns a restore function. Concurrency mirrors Quiet: call once on the main
// goroutine before spawning concurrent work, defer the restore in the same one.
func AddErrSink(sink io.Writer) func() {
	mu.Lock()
	prev := errW
	errW = io.MultiWriter(prev, sink)
	mu.Unlock()
	return func() {
		mu.Lock()
		errW = prev
		mu.Unlock()
	}
}

// Quiet replaces stdout with io.Discard and returns a cleanup function.
// Usage:
//
//	defer console.Quiet()()
//
// WARNING: Quiet must ONLY be called from the main goroutine, before spawning
// any concurrent work that writes to stdout, and its returned cleanup must be
// deferred in the same goroutine. Never call Quiet from multiple goroutines
// concurrently — it is not designed for nested or parallel silencing.
func Quiet() func() {
	mu.Lock()
	old := w
	w = io.Discard
	mu.Unlock()
	return func() {
		mu.Lock()
		w = old
		mu.Unlock()
	}
}
