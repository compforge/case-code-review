package harness

import (
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/qiankunli/case-code-review/internal/stdout"
)

// WorkerPool bounds asynchronous work requested by tool handlers. Harness owns
// execution and failure isolation; domain handlers own result storage.
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
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(stdout.Writer(), "[ccr] WorkerPool panic: %v\n%s\n", r, debug.Stack())
			}
		}()

		if err := f(); err != nil {
			fmt.Fprintf(stdout.Writer(), "[ccr] WorkerPool error: %v\n", err)
		}
	})
}

// Await blocks until all submitted work has completed.
//
// Await must not run concurrently with Submit. Submit calls wg.Go, which adds
// synchronously before starting work; racing that Add with Wait is invalid.
func (p *WorkerPool) Await() {
	p.wg.Wait()
}
