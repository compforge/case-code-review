package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/compforge/agentgo"
	models "github.com/compforge/agentgo/llm"
	"github.com/qiankunli/case-code-review/internal/console"
)

const (
	defaultRoutingCallTimeout = 180 * time.Second
	policyPriority            = models.RoutePriority
	policyRoundRobin          = models.RouteRoundRobin
)

// NewLLMRouter assembles application endpoints; selection, cooldown and failover
// are owned by AgentGo. Each member has exactly one transport attempt.
//
// +spec=`Routing preserves each candidate's configured model and shares one call deadline; default endpoint slices leave time for fallback`
// +link=component://docs/harness.md
func NewLLMRouter(eps []ResolvedEndpoint, routing RoutingOptions) LLMClient {
	if len(eps) == 1 {
		return NewLLMClient(eps[0])
	}
	callTimeout := routing.CallTimeout
	if callTimeout <= 0 {
		callTimeout = defaultRoutingCallTimeout
	}
	attemptTimeout := routing.AttemptTimeout
	if attemptTimeout == 0 && len(eps) > 0 {
		attemptTimeout = callTimeout / time.Duration(len(eps))
	}
	routes := make([]models.Route, 0, len(eps))
	for i, ep := range eps {
		ep.MaxRetries = -1
		client := NewLLMClient(ep).(*modelClient)
		if client.err != nil {
			return client
		}
		if client.model == nil {
			return &modelClient{err: fmt.Errorf("llm routing endpoint %d requires a model", i+1)}
		}
		name := ep.Alias
		if name == "" {
			name = fmt.Sprintf("%s/%s#%d", ep.Protocol, ep.Model, i+1)
		}
		routes = append(routes, models.Route{Name: name, Model: client.model})
	}
	router, err := models.NewRouter(routes, models.RouterConfig{Policy: routing.Policy, CallTimeout: callTimeout, AttemptTimeout: attemptTimeout,
		Observe: func(_ context.Context, attempt agentgo.ModelAttempt, err error) {
			if err != nil {
				fmt.Fprintf(console.Err(), "[llm-router] endpoint=%s attempt=%d duration=%s error=%s timeout_scope=%s failover=%t\n", attempt.Endpoint, attempt.Index, attempt.Duration.Round(time.Millisecond), attempt.ErrorKind, attempt.TimeoutScope, attempt.WillFailover)
			}
		},
	})
	return &modelClient{model: router, err: err}
}
