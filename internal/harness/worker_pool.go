package harness

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/qiankunli/case-code-review/internal/console"
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
	p.SubmitContext(context.Background(), f, nil)
}

// SubmitContext bounds queueing by ctx. Exactly one of f or onCanceled runs;
// onCanceled must only perform local finalization, without acquiring a worker.
// Once admitted, f owns cancellation of its running work.
func (p *WorkerPool) SubmitContext(ctx context.Context, f func() error, onCanceled func()) {
	p.wg.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(console.Out(), "[ccr] WorkerPool panic: %v\n%s\n", r, debug.Stack())
			}
		}()
		admitted := false
		select {
		case p.semaphore <- struct{}{}:
			admitted = true
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			if admitted {
				<-p.semaphore
			}
			if onCanceled != nil {
				onCanceled()
			}
			return
		}
		defer func() { <-p.semaphore }()
		if err := f(); err != nil {
			fmt.Fprintf(console.Out(), "[ccr] WorkerPool error: %v\n", err)
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
