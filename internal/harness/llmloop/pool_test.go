package llmloop

import (
	"errors"
	"testing"
)

func TestWorkerPool_PanicAndErrorAreIsolated(t *testing.T) {
	p := NewWorkerPool(2)

	p.Submit(func() error {
		panic("boom in submitted work")
	})
	p.Submit(func() error {
		return errors.New("healthy worker error")
	})

	// Await must not crash; hook-specific results live outside this generic pool.
	p.Await()
}
