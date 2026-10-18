package llm

import (
	"context"
	"errors"
	"testing"

	"time"
)

func TestRequestTraceDistinguishesPoolAndConnect(t *testing.T) {
	progress := testRequestTrace(t)
	progress.current.transition(requestPhaseConnectionPool)
	attributes := progress.attributes()
	if got := attributes["request_phase"]; got != requestPhaseConnectionPool {
		t.Fatalf("request_phase = %v, want %s", got, requestPhaseConnectionPool)
	}
	if got := failurePhaseForRequest(attributes); got != requestPhaseConnectionPool {
		t.Fatalf("failure phase = %v, want %s", got, requestPhaseConnectionPool)
	}
	progress.current.transition(requestPhaseConnect)
	attributes = progress.attributes()
	if got := attributes["request_phase"]; got != requestPhaseConnect {
		t.Fatalf("request_phase = %v, want %s", got, requestPhaseConnect)
	}
	if got := failurePhaseForRequest(attributes); got != requestPhaseConnect {
		t.Fatalf("failure phase = %v, want %s", got, requestPhaseConnect)
	}
}

func TestAnnotateRequestTimeoutBeforeResponse(t *testing.T) {
	progress := testRequestTrace(t)
	progress.current.connected(false)
	progress.current.transition(requestPhaseAwaitResponse)

	err := annotateRequestError(context.Background(), context.DeadlineExceeded, progress)
	details := DescribeError(err)
	if details == nil {
		t.Fatal("timeout did not produce structured details")
	}
	if details.Kind != "llm" || details.Phase != "request" ||
		details.ErrorType != "timeout" || details.Code != "request_timeout" {
		t.Fatalf("details = %+v", details)
	}
	if got := details.Attributes["request_phase"]; got != requestPhaseAwaitResponse {
		t.Fatalf("request_phase = %v, want %s", got, requestPhaseAwaitResponse)
	}
	if got := details.Attributes["response_started"]; got != false {
		t.Fatalf("response_started = %v, want false", got)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wrapped error lost deadline cause: %v", err)
	}
}

func TestAnnotateRequestTimeoutWhileReadingResponse(t *testing.T) {
	progress := testRequestTrace(t)
	progress.current.connected(true)
	progress.current.transition(requestPhaseAwaitResponse)
	progress.current.transition(requestPhaseResponseRead)

	err := annotateRequestError(context.Background(), context.DeadlineExceeded, progress)
	details := DescribeError(err)
	if details == nil {
		t.Fatal("timeout did not produce structured details")
	}
	if got := details.Attributes["request_phase"]; got != requestPhaseResponseRead {
		t.Fatalf("request_phase = %v, want %s", got, requestPhaseResponseRead)
	}
	if got := details.Attributes["response_started"]; got != true {
		t.Fatalf("response_started = %v, want true", got)
	}
}

func testRequestTrace(t *testing.T) *requestTrace {
	t.Helper()
	return &requestTrace{attempts: 1, current: &httpAttempt{started: time.Now(), facts: map[string]any{}}}
}

func TestResponseObservationSurvivesLateWriteCallback(t *testing.T) {
	trace := testRequestTrace(t)
	trace.current.transition(requestPhaseWriteRequest)
	trace.current.transition(requestPhaseResponseRead)
	trace.current.wroteRequest()
	attributes := trace.attributes()
	if attributes["request_phase"] != requestPhaseResponseRead || attributes["request_written"] != true || attributes["response_started"] != true {
		t.Fatalf("late write lost observed response: %v", attributes)
	}
}
