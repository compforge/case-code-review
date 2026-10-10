package runner

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/qiankunli/case-code-review/internal/config/template"
)

func TestUnitBudgetSharesDeadlineAndRecomputesVerificationReserve(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		began := time.Now()
		b := newUnitBudget(context.Background(), 10*time.Minute, template.ReviewTimeBudget{}, true)
		defer b.close()
		time.Sleep(3 * time.Minute)
		b.finishDiscovery()
		if b.discoveryCtx.Err() != context.Canceled || b.reviewCtx.Err() != nil {
			t.Fatal("R1 completion canceled R2")
		}
		if !b.deadline.Equal(began.Add(10 * time.Minute)) {
			t.Fatal("Unit deadline changed")
		}
		end, _ := b.reviewCtx.Deadline()
		if !end.Equal(b.deadline.Add(-2 * time.Second)) {
			t.Fatalf("review end=%s", end)
		}
		expected := time.Now().Add(time.Duration(float64(end.Sub(time.Now())) * .85))
		if !b.wrapUpDeadline().Equal(expected) {
			t.Fatalf("wrap-up=%s want %s", b.wrapUpDeadline(), expected)
		}
		time.Sleep(end.Sub(time.Now()))
		synctest.Wait()
		if b.reviewCtx.Err() != context.DeadlineExceeded || b.ctx.Err() != nil {
			t.Fatal("Trial tail not reserved")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if b.ctx.Err() != context.DeadlineExceeded {
			t.Fatal("Unit deadline missing")
		}
	})
}

func TestUnitBudgetUnlimitedAndCallerDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		unlimited := newUnitBudget(context.Background(), 0, template.ReviewTimeBudget{}, true)
		defer unlimited.close()
		if _, ok := unlimited.ctx.Deadline(); ok {
			t.Fatal("zero timeout acquired a deadline")
		}
		unlimited.finishDiscovery()
		if unlimited.reviewCtx.Err() != nil || !unlimited.wrapUpDeadline().IsZero() {
			t.Fatal("unlimited verification was bounded")
		}
		parent, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		bounded := newUnitBudget(parent, 10*time.Minute, template.ReviewTimeBudget{}, true)
		defer bounded.close()
		deadline, _ := parent.Deadline()
		if !bounded.deadline.Equal(deadline) || !bounded.discoveryEnd.Before(deadline) {
			t.Fatal("caller deadline was extended")
		}
		cancel()
		if bounded.discoveryCtx.Err() != context.Canceled || bounded.reviewCtx.Err() != context.Canceled {
			t.Fatal("caller cancellation lost")
		}
	})
}
