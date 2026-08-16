package llm

import (
	"context"
	"errors"
	"testing"
)

func TestRequestProgressDistinguishesPoolAndConnect(t *testing.T) {
	progress := &requestProgress{}
	progress.beginAttempt()
	progress.waitForConnection()
	attributes := progress.attributes()
	if got := attributes["request_phase"]; got != requestPhaseConnectionPool {
		t.Fatalf("request_phase = %v, want %s", got, requestPhaseConnectionPool)
	}
	if got := failurePhaseForRequest(attributes); got != requestPhaseConnectionPool {
		t.Fatalf("failure phase = %v, want %s", got, requestPhaseConnectionPool)
	}
	progress.startConnect()
	attributes = progress.attributes()
	if got := attributes["request_phase"]; got != requestPhaseConnect {
		t.Fatalf("request_phase = %v, want %s", got, requestPhaseConnect)
	}
	if got := failurePhaseForRequest(attributes); got != requestPhaseConnect {
		t.Fatalf("failure phase = %v, want %s", got, requestPhaseConnect)
	}
}

func TestAnnotateRequestTimeoutBeforeResponse(t *testing.T) {
	progress := &requestProgress{}
	progress.beginAttempt()
	progress.gotConnection(false)
	progress.wroteRequest()

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
	progress := &requestProgress{}
	progress.beginAttempt()
	progress.gotConnection(true)
	progress.wroteRequest()
	progress.gotFirstResponseByte()

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
