package llm

import (
	"context"

	"github.com/compforge/agentgo"
)

// observeStream forwards provider progress unchanged and observes one terminal
// outcome. sourceEnded distinguishes provider completion from caller cancellation;
// a canceled router may still report its admitted attempt's usage later.
func observeStream(ctx context.Context, source <-chan agentgo.StreamEvent, finish func(agentgo.Message, error, bool) (agentgo.Message, error)) <-chan agentgo.StreamEvent {
	output := make(chan agentgo.StreamEvent, 1)
	go func() {
		defer close(output)
		var partial agentgo.Message
		terminal := func(event agentgo.StreamEvent, sourceEnded bool) {
			event.Message, event.Err = finish(event.Message, event.Err, sourceEnded)
			if event.Err != nil {
				event.Type = agentgo.StreamEventError
			}
			select {
			case output <- event:
			case <-ctx.Done():
			}
		}
		for {
			select {
			case <-ctx.Done():
				terminal(agentgo.StreamEvent{Type: agentgo.StreamEventError, Message: partial, Err: ctx.Err()}, false)
				return
			case event, ok := <-source:
				if !ok {
					terminal(agentgo.StreamEvent{Type: agentgo.StreamEventError, Message: partial, Err: &agentgo.PartialStreamError{Partial: partial}}, true)
					return
				}
				if event.Message.Role != "" || len(event.Message.Content) > 0 || event.Message.Usage != nil {
					partial = event.Message
				}
				if event.Type == agentgo.StreamEventDone || event.Type == agentgo.StreamEventError {
					event.Message = partial
					if event.Type == agentgo.StreamEventError && event.Err == nil {
						event.Err = &agentgo.PartialStreamError{Partial: partial}
					}
					terminal(event, true)
					return
				}
				select {
				case output <- event:
				case <-ctx.Done():
					terminal(agentgo.StreamEvent{Type: agentgo.StreamEventError, Message: partial, Err: ctx.Err()}, false)
					return
				}
			}
		}
	}()
	return output
}
