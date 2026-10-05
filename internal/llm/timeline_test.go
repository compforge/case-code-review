package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/compforge/go-stdx/timeline"
)

func recordedRequest(t *testing.T, run func(context.Context) error) timeline.Snapshot {
	t.Helper()
	recorder, err := timeline.New("test-request")
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Start(context.Background(), "llm.request"); err != nil {
		t.Fatal(err)
	}
	err = run(timeline.NewContext(context.Background(), recorder))
	snapshot, collectErr := recorder.Finish(context.Background(), err)
	if collectErr != nil {
		t.Fatal(collectErr)
	}
	return snapshot
}

func TestRequestTimelinePreservesFailedFallbackAndSuccessfulResult(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 503) }))
	defer failing.Close()
	success := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"ok","model":"test","choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
	}))
	defer success.Close()
	router := NewLLMRouter([]ResolvedEndpoint{
		{Protocol: "openai", URL: failing.URL + "/v1/chat/completions", Model: "a", Alias: "primary", Timeout: time.Second},
		{Protocol: "openai", URL: success.URL + "/v1/chat/completions", Model: "b", Alias: "fallback", Timeout: time.Second},
	}, RoutingOptions{CallTimeout: time.Second})
	snapshot := recordedRequest(t, func(ctx context.Context) error {
		response, err := router.CompletionsWithCtx(ctx, ChatRequest{Messages: []Message{NewTextMessage("user", "hi")}})
		if err != nil {
			t.Fatal(err)
		}
		if response.Alias != "fallback" {
			t.Fatalf("alias=%q", response.Alias)
		}
		return err
	})
	if snapshot.Status != timeline.Succeeded {
		t.Fatalf("root status=%s", snapshot.Status)
	}
	counts := map[string]int{}
	var attempts []timeline.Stage
	for _, s := range snapshot.Stages {
		counts[s.Name]++
		if s.Status == timeline.Running || s.FinishedAt.IsZero() {
			t.Fatalf("unfinished stage: %+v", s)
		}
		if s.Name == "llm.attempt" {
			attempts = append(attempts, s)
		}
	}
	if len(attempts) != 2 || attempts[0].Status != timeline.Failed || attempts[1].Status != timeline.Succeeded {
		t.Fatalf("attempts=%+v", attempts)
	}
	if counts["http.request"] != 2 || counts[requestPhaseResponseRead] != 2 {
		t.Fatalf("stages=%v", counts)
	}
	for _, attempt := range attempts {
		if _, ok := timeline.AttributeValue[int64](attempt.Attributes, "remaining_budget_ms"); !ok {
			t.Fatalf("missing budget: %+v", attempt)
		}
	}
}

func TestRequestTimelineTimeoutRecordsAwaitResponseAfterCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := NewOpenAIClient(ClientConfig{URL: server.URL + "/v1/chat/completions", Model: "test", MaxRetries: -1})
	snapshot := recordedRequest(t, func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		_, err := client.CompletionsWithCtx(ctx, ChatRequest{Messages: []Message{NewTextMessage("user", "hi")}})
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
	found := false
	for _, s := range snapshot.Stages {
		if s.Status == timeline.Running {
			t.Fatalf("unfinished: %+v", s)
		}
		if s.Name == requestPhaseAwaitResponse {
			found = true
			if s.Status != timeline.Canceled {
				t.Fatalf("wait=%+v", s)
			}
		}
	}
	if !found {
		t.Fatal("missing await_response")
	}
}

func TestRequestTimelineHTTPRetriesHaveSeparateStages(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("retry-after-ms", "1")
			http.Error(w, "retry", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"ok","model":"test","choices":[{"message":{"role":"assistant","content":"done"}}]}`)
	}))
	defer server.Close()
	client := NewOpenAIClient(ClientConfig{URL: server.URL + "/v1/chat/completions", Model: "test", MaxRetries: 1})
	snapshot := recordedRequest(t, func(ctx context.Context) error {
		_, err := client.CompletionsWithCtx(ctx, ChatRequest{Messages: []Message{NewTextMessage("user", "hi")}})
		return err
	})
	if snapshot.Status != timeline.Succeeded {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	var attempts []timeline.Stage
	for _, s := range snapshot.Stages {
		if s.Name == "http.request" {
			attempts = append(attempts, s)
		}
	}
	if len(attempts) != 2 || attempts[0].Status != timeline.Failed || attempts[1].Status != timeline.Succeeded {
		t.Fatalf("HTTP attempts=%+v", attempts)
	}
}

func TestRequestTimelineSeparatesResponseBodyWait(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := NewOpenAIClient(ClientConfig{URL: server.URL + "/v1/chat/completions", Model: "test", MaxRetries: -1})
	snapshot := recordedRequest(t, func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		_, err := client.CompletionsWithCtx(ctx, ChatRequest{Messages: []Message{NewTextMessage("user", "hi")}})
		details := DescribeError(err)
		if details == nil || details.Attributes["request_phase"] != requestPhaseResponseRead {
			t.Fatalf("error=%v details=%+v", err, details)
		}
		return err
	})
	for _, s := range snapshot.Stages {
		if s.Name == requestPhaseResponseRead && s.Status == timeline.Canceled {
			return
		}
	}
	t.Fatal("missing failed response_read stage")
}
