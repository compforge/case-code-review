package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/compforge/agentgo"
	"github.com/compforge/go-stdx/timeline"

	"github.com/qiankunli/case-code-review/internal/telemetry"
)

func recordedRequest(t *testing.T, run func(context.Context) error) timeline.Snapshot {
	t.Helper()
	id := t.Name()
	defer telemetry.ReleaseTimeline(id)
	if err := timeline.Start(id, "llm.request"); err != nil {
		t.Fatal(err)
	}
	err := run(timeline.NewStageContext(context.Background(), timeline.StageRef{TimelineID: id}))
	if finishErr := timeline.Finish(id, err); finishErr != nil {
		t.Fatal(finishErr)
	}
	snapshot, collectErr := timeline.Read(context.Background(), id, false)
	if collectErr != nil {
		t.Fatal(collectErr)
	}
	return snapshot
}

func TestModelCallFallbackDoesNotRecordTransportStages(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 503) }))
	defer failing.Close()
	success := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeModelStream(t, w, `{"id":"ok","model":"test","choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
	}))
	defer success.Close()
	router := NewRouter([]ResolvedEndpoint{
		{Protocol: "openai", URL: failing.URL + "/v1/chat/completions", Model: "a", Alias: "primary", Timeout: time.Second},
		{Protocol: "openai", URL: success.URL + "/v1/chat/completions", Model: "b", Alias: "fallback", Timeout: time.Second},
	}, RoutingOptions{CallTimeout: time.Second})
	snapshot := recordedRequest(t, func(ctx context.Context) error {
		response, err := Call(ctx, router, agentgo.LLMRequest{Messages: []agentgo.Message{{Role: agentgo.RoleUser, Content: []agentgo.ContentBlock{agentgo.TextBlock("hi")}}}, Config: &agentgo.CallConfig{}})
		if err != nil {
			t.Fatal(err)
		}
		if ResponseAlias(response) != "fallback" {
			t.Fatalf("alias=%q", ResponseAlias(response))
		}
		return err
	})
	if snapshot.Status != timeline.Succeeded {
		t.Fatalf("root status=%s", snapshot.Status)
	}
	if len(snapshot.Stages) != 0 {
		t.Fatalf("transport stages leaked into business timeline: %+v", snapshot.Stages)
	}
}

func TestModelCallTimeoutPreservesNetworkErrorPhase(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := newOpenAIModel(ModelConfig{URL: server.URL + "/v1/chat/completions", Model: "test"})
	snapshot := recordedRequest(t, func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		_, err := Call(ctx, client, agentgo.LLMRequest{Messages: []agentgo.Message{{Role: agentgo.RoleUser, Content: []agentgo.ContentBlock{agentgo.TextBlock("hi")}}}, Config: &agentgo.CallConfig{}})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error=%v", err)
		}
		details := DescribeError(err)
		if details == nil || details.Attributes["request_phase"] != requestPhaseAwaitResponse || details.Attributes["response_started"] != false {
			t.Fatalf("details=%+v", details)
		}
		return err
	})
	if snapshot.Status != timeline.Canceled {
		t.Fatalf("status=%s", snapshot.Status)
	}
	if len(snapshot.Stages) != 0 {
		t.Fatalf("transport stages leaked: %+v", snapshot.Stages)
	}
}

func TestModelCallDoesNotRetryInsideProvider(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("retry-after-ms", "1")
			http.Error(w, "retry", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		writeModelStream(t, w, `{"id":"ok","model":"test","choices":[{"message":{"role":"assistant","content":"done"}}]}`)
	}))
	defer server.Close()
	client := newOpenAIModel(ModelConfig{URL: server.URL + "/v1/chat/completions", Model: "test"})
	snapshot := recordedRequest(t, func(ctx context.Context) error {
		_, err := Call(ctx, client, agentgo.LLMRequest{Messages: []agentgo.Message{{Role: agentgo.RoleUser, Content: []agentgo.ContentBlock{agentgo.TextBlock("hi")}}}, Config: &agentgo.CallConfig{}})
		return err
	})
	if snapshot.Status != timeline.Failed {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if calls.Load() != 1 || len(snapshot.Stages) != 0 {
		t.Fatalf("calls=%d stages=%+v", calls.Load(), snapshot.Stages)
	}
}

func TestModelCallTimeoutPreservesResponseReadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := newOpenAIModel(ModelConfig{URL: server.URL + "/v1/chat/completions", Model: "test"})
	snapshot := recordedRequest(t, func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		_, err := Call(ctx, client, agentgo.LLMRequest{Messages: []agentgo.Message{{Role: agentgo.RoleUser, Content: []agentgo.ContentBlock{agentgo.TextBlock("hi")}}}, Config: &agentgo.CallConfig{}})
		details := DescribeError(err)
		if details == nil || details.Attributes["request_phase"] != requestPhaseResponseRead {
			t.Fatalf("error=%v details=%+v", err, details)
		}
		return err
	})
	if snapshot.Status != timeline.Canceled || len(snapshot.Stages) != 0 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}
