package llm

import (
	"context"

	"github.com/compforge/agentgo"
)

// Call dispatches the exact native request captured by the caller's recorder.
func Call(ctx context.Context, model agentgo.ChatModel, request agentgo.LLMRequest) (*agentgo.LLMResponse, error) {
	executor := agentgo.NewModelExecutor(agentgo.ModelExecutorConfig{Middlewares: []agentgo.ModelMiddleware{ModelRequestMiddleware}})
	result, err := executor.Call(ctx, model, agentgo.ModelExecution{
		Execution: agentgo.Execution{ID: "model-1", Kind: agentgo.ExecutionKindModel, Attempt: 1}, Request: request,
	})
	if err != nil && result.Message.Role == "" && len(result.Message.Content) == 0 && result.Message.Usage == nil {
		return nil, err
	}
	return &agentgo.LLMResponse{Message: result.Message}, err
}

// CompletionStream adapts an aggregate-only model or test fixture to ChatModel.
// A failed completion may still carry partial content and reported usage.
func CompletionStream(ctx context.Context, generate func() (*agentgo.LLMResponse, error)) (<-chan agentgo.StreamEvent, error) {
	events := make(chan agentgo.StreamEvent, 1)
	go func() {
		defer close(events)
		response, err := generate()
		event := agentgo.StreamEvent{Type: agentgo.StreamEventDone}
		if response != nil {
			event.Message = response.Message
			event.StopReason = response.Message.StopReason
		}
		if err != nil {
			event.Type = agentgo.StreamEventError
			event.Err = err
		}
		// The single buffered terminal event must survive cancellation so the
		// loop can distinguish deadline recovery from a broken stream.
		events <- event
	}()
	return events, nil
}

type attemptObserverKey struct{}
type attemptObserver func(context.Context, agentgo.ModelAttempt, error)

// ObserveAttempts adds a call-local consumer. Router selection remains in AgentGo.
func ObserveAttempts(ctx context.Context, observe func(context.Context, agentgo.ModelAttempt, error)) context.Context {
	previous, _ := ctx.Value(attemptObserverKey{}).(attemptObserver)
	return context.WithValue(ctx, attemptObserverKey{}, attemptObserver(func(ctx context.Context, fact agentgo.ModelAttempt, err error) {
		if previous != nil {
			previous(ctx, fact, err)
		}
		observe(ctx, fact, err)
	}))
}
func reportAttempt(ctx context.Context, fact agentgo.ModelAttempt, err error) {
	if observe, ok := ctx.Value(attemptObserverKey{}).(attemptObserver); ok {
		observe(ctx, fact, err)
	}
}

func ResponseModel(response *agentgo.LLMResponse) string {
	if response == nil {
		return ""
	}
	if response.Message.Usage != nil && response.Message.Usage.Model != "" {
		return response.Message.Usage.Model
	}
	name, _ := response.Message.Metadata["model"].(string)
	return name
}
func ResponseAlias(response *agentgo.LLMResponse) string {
	if response == nil {
		return ""
	}
	name, _ := response.Message.Metadata["route_endpoint"].(string)
	return name
}
