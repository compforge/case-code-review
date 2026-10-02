package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
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
		// Both providers use non-streaming SDK calls, so await/read progress cannot
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

func routingTimeoutError(
	cause error,
	budget time.Duration,
	member routerMember,
	attempt int,
	poolSize int,
) error {
	attributes := map[string]any{}
	if details := DescribeError(cause); details != nil {
		for key, value := range details.Attributes {
			attributes[key] = value
		}
	}
	endpoint := member.alias
	if endpoint == "" {
		endpoint = member.label
	}
	attributes["timeout_scope"] = "routing"
	attributes["endpoint"] = endpoint
	attributes["attempt"] = attempt
	attributes["pool_size"] = poolSize
	attributes["budget_ms"] = budget.Milliseconds()
	phase := requestPhase(attributes)
	responseStarted, _ := attributes["response_started"].(bool)
	return &detailedError{
		details: ErrorDetails{
			Kind: "llm", Phase: "routing", ErrorType: "timeout",
			Code: "routing_budget_exhausted", Attributes: attributes,
		},
		cause: cause,
		text: fmt.Sprintf(
			"llm routing call timeout exceeded after %s (endpoint=%s, attempt=%d/%d, request_phase=%s, response_started=%t): %v",
			budget, endpoint, attempt, poolSize, phase, responseStarted, cause,
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
