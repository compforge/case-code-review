package harness

import (
	"errors"
	"testing"
)

func TestWorkerPoolIsolatesPanicsAndErrors(t *testing.T) {
	pool := NewWorkerPool(2)
	pool.Submit(func() error {
		panic("boom in submitted work")
	})
	pool.Submit(func() error {
		return errors.New("healthy worker error")
	})

	pool.Await()
}
