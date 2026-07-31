// Package llmloop carries the LLM tool-use loop shared by Runner modes.
package llmloop

import (
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/qiankunli/case-code-review/internal/console"
)

// Warning describes a non-fatal warning recorded during an execution.
type Warning struct {
	File    string `json:"file"`
	Message string `json:"message"`
	Type    string `json:"type"`
}

// WorkerPool bounds asynchronous work requested by tool hooks. The Harness
// owns execution and failure isolation; domain hooks own result storage.
type WorkerPool struct {
	semaphore chan struct{}
	wg        sync.WaitGroup
}

// NewWorkerPool creates a pool with the given concurrency limit.
// workerCount <= 0 defaults to 8.
func NewWorkerPool(workerCount int) *WorkerPool {
	if workerCount <= 0 {
		workerCount = 8
	}
	return &WorkerPool{
		semaphore: make(chan struct{}, workerCount),
	}
}

// Submit runs f in a background goroutine bounded by the semaphore.
func (p *WorkerPool) Submit(f func() error) {
	p.wg.Go(func() {
		p.semaphore <- struct{}{}
		defer func() { <-p.semaphore }()
		// Contain a panic in the submitted work so one bad unit of work cannot
		// crash the whole process; the semaphore is still released.
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(console.Out(), "[ccr] WorkerPool panic: %v\n%s\n", r, debug.Stack())
			}
		}()

		err := f()
		if err != nil {
			fmt.Fprintf(console.Out(), "[ccr] WorkerPool error: %v\n", err)
		}
	})
}

// Await blocks until all submitted work has completed.
//
// A panic in submitted work is recovered and logged inside Submit (see the
// recover defer there) but is not surfaced here as an error.
//
// Concurrency contract: Await must not run concurrently with Submit. Submit
// calls wg.Go (which does wg.Add(1) synchronously), so a Submit racing Await
// would risk sync.WaitGroup's "Add called concurrently with Wait" panic.
// Callers must ensure every Submit has returned before calling Await.
func (p *WorkerPool) Await() {
	p.wg.Wait()
}
