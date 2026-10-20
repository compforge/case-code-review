package llm

import (
	"context"
	"errors"
	"fmt"
	"net"

	models "github.com/compforge/agentgo/llm"
)

// ErrorDetails is the stable failure projection persisted with an LLM error.
// Attributes retain transport progress without making endpoint timing part of
// the low-cardinality Failure taxonomy.
type ErrorDetails struct {
	Kind       string         `json:"kind"`
	Phase      string         `json:"phase"`
	ErrorType  string         `json:"error_type"`
	Code       string         `json:"code,omitempty"`
	Message    string         `json:"message"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

type errorDetailer interface {
	errorDetails() ErrorDetails
}

type detailedError struct {
	details ErrorDetails
	cause   error
	text    string
}

func (e *detailedError) Error() string { return e.text }
func (e *detailedError) Unwrap() error { return e.cause }
func (e *detailedError) errorDetails() ErrorDetails {
	details := e.details
	details.Message = e.text
	details.Attributes = cloneAttributes(details.Attributes)
	return details
}

// DescribeError returns structured details only for errors classified by this
// package. Unknown provider failures retain their original text until a stable
// cross-provider classification exists.
func DescribeError(err error) *ErrorDetails {
	var route *models.RoutingError
	if errors.As(err, &route) {
		fact := route.Attempt
		attrs := map[string]any{"timeout_scope": fact.TimeoutScope, "endpoint": fact.Endpoint, "attempt": fact.Index, "duration_ms": fact.Duration.Milliseconds()}
		var source errorDetailer
		if errors.As(err, &source) {
			for k, v := range source.errorDetails().Attributes {
				if _, exists := attrs[k]; !exists {
					attrs[k] = v
				}
			}
		}
		code := "routing_failed"
		kind := fact.ErrorKind
		if fact.TimeoutScope == "routing" {
			code = "routing_budget_exhausted"
			kind = "timeout"
		}
		if fact.TimeoutScope == "caller" {
			code = "caller_deadline_exceeded"
		}
		return &ErrorDetails{Kind: "llm", Phase: "routing", ErrorType: kind, Code: code, Message: err.Error(), Attributes: attrs}
	}
	var source errorDetailer
	if !errors.As(err, &source) {
		return nil
	}
	details := source.errorDetails()
	return &details
}

func requestPhase(attributes map[string]any) string {
	phase, _ := attributes["request_phase"].(string)
	if phase == "" {
		return "unknown"
	}
	return phase
}

func failurePhaseForRequest(attributes map[string]any) string {
	phase := requestPhase(attributes)
	switch phase {
	case requestPhaseConnectionPool, requestPhaseConnect, requestPhaseWriteRequest:
		return phase
	default:
		// CCR uses non-streaming model calls, so await/read progress cannot
		// truthfully be classified as first_chunk or inter_chunk.
		return "request"
	}
}

func annotateRequestError(ctx context.Context, err error, progress *requestTrace) error {
	if err == nil || !isTimeout(err) {
		return err
	}
	attributes := progress.attributes()
	attributes["timeout_scope"] = "endpoint"
	code := "request_timeout"
	if ctx.Err() != nil {
		attributes["timeout_scope"] = "caller"
		code = "caller_deadline_exceeded"
	}
	phase := requestPhase(attributes)
	return &detailedError{
		details: ErrorDetails{
			Kind: "llm", Phase: failurePhaseForRequest(attributes), ErrorType: "timeout", Code: code,
			Attributes: attributes,
		},
		cause: err,
		text: fmt.Sprintf(
			"llm request timeout (request_phase=%s, response_started=%t): %v",
			phase, attributes["response_started"], err,
		),
	}
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func cloneAttributes(source map[string]any) map[string]any {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
