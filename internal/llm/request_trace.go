package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync"

	"github.com/compforge/go-stdx/timeline"
	"github.com/qiankunli/case-code-review/internal/telemetry"
)

const (
	requestPhaseUnknown        = "unknown"
	requestPhaseConnectionPool = "connection_pool"
	requestPhaseConnect        = "connect"
	requestPhaseWriteRequest   = "request_write"
	requestPhaseAwaitResponse  = "await_response"
	requestPhaseResponseRead   = "response_read"
)

// requestTrace adapts HTTP callbacks to native timeline stages. The handles only
// track active work; timing and completed facts belong to timeline.
type requestTrace struct {
	ctx      context.Context
	current  *httpAttempt
	attempts int
}

type httpAttempt struct {
	mu    sync.Mutex
	ctx   context.Context
	stage timeline.StageHandle
	phase timeline.StageHandle
	name  string
	done  bool
}

func newRequestTrace(ctx context.Context) *requestTrace { return &requestTrace{ctx: ctx} }

func (p *requestTrace) middleware(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	p.attempts++
	ctx, stage := beginStage(p.ctx, "http.request", append(remainingBudget(req.Context()), timeline.Attribute{Key: "http_attempt", Value: p.attempts})...)
	a := &httpAttempt{ctx: ctx, stage: stage}
	p.current = a
	a.transition(requestPhaseConnectionPool)
	trace := &httptrace.ClientTrace{
		GetConn:      func(string) { a.transition(requestPhaseConnectionPool) },
		DNSStart:     func(httptrace.DNSStartInfo) { a.transition(requestPhaseConnect) },
		ConnectStart: func(string, string) { a.transition(requestPhaseConnect) },
		GotConn: func(info httptrace.GotConnInfo) {
			a.transition(requestPhaseWriteRequest, timeline.Attribute{Key: "connection_reused", Value: info.Reused})
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err != nil {
				a.finish(info.Err)
				return
			}
			a.wroteRequest()
		},
		GotFirstResponseByte: func() { a.transition(requestPhaseResponseRead) },
	}
	resp, err := next(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
	if err != nil {
		a.finish(err)
		return resp, err
	}
	if resp == nil {
		return resp, err
	}
	a.transition(requestPhaseResponseRead)
	var statusErr error
	if resp.StatusCode >= 400 {
		statusErr = fmt.Errorf("HTTP status %d", resp.StatusCode)
	}
	if resp.Body == nil {
		a.finish(statusErr)
	} else {
		// RoundTrip returns at headers. Keep response_read open until body EOF,
		// read failure or Close, including failed responses retried by the SDK.
		resp.Body = &timelineBody{ReadCloser: resp.Body, attempt: a, statusErr: statusErr}
	}
	return resp, err
}

func (a *httpAttempt) transition(name string, attributes ...timeline.Attribute) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// A response can arrive while the transport is still reporting the write.
	// Once observed, a late write/connect callback cannot put it back in a wait.
	if a.done || a.name == name || a.name == requestPhaseResponseRead {
		return
	}
	if a.phase != nil {
		endStage(a.ctx, a.phase, nil)
	}
	_, a.phase = beginStage(a.ctx, name, attributes...)
	a.name = name
}

func (a *httpAttempt) wroteRequest() {
	a.stage.SetAttributes(timeline.Attribute{Key: "request_written", Value: true})
	a.transition(requestPhaseAwaitResponse)
}

func (a *httpAttempt) finish(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.done {
		return
	}
	a.done = true
	if a.phase != nil {
		endStage(a.ctx, a.phase, err)
	}
	endStage(a.ctx, a.stage, err)
}

func (p *requestTrace) finish(err error) {
	if p.current != nil {
		p.current.finish(err)
	}
}

type timelineBody struct {
	io.ReadCloser
	attempt   *httpAttempt
	statusErr error
}

func (b *timelineBody) Read(buf []byte) (int, error) {
	n, err := b.ReadCloser.Read(buf)
	if err == io.EOF {
		b.attempt.finish(b.statusErr)
	} else if err != nil {
		b.attempt.finish(err)
	}
	return n, err
}

func (b *timelineBody) Close() error {
	err := b.ReadCloser.Close()
	if err != nil {
		b.attempt.finish(err)
	} else {
		b.attempt.finish(b.statusErr)
	}
	return err
}

// attributes projects the final HTTP attempt for the stable failure taxonomy.
// Successful calls and previous fallback attempts retain the same stage facts.
func (p *requestTrace) attributes() map[string]any {
	attrs := map[string]any{"request_phase": requestPhaseUnknown, "http_attempts": p.attempts, "connection_reused": false, "request_written": false, "response_started": false}
	if p.current == nil {
		return attrs
	}
	p.current.mu.Lock()
	defer p.current.mu.Unlock()
	ref, _ := timeline.StageFromContext(p.ctx)
	snapshot, err := timeline.Read(context.Background(), ref.TimelineID, false)
	telemetry.ReportTimelineError(err)
	var request timeline.Stage
	for _, stage := range snapshot.Stages {
		if stage.ID == p.current.stage.ID() {
			request = stage
			break
		}
	}
	attrs["request_written"], _ = timeline.AttributeValue[bool](request.Attributes, "request_written")
	for _, stage := range snapshot.Stages {
		if stage.ParentID != request.ID {
			continue
		}
		if p.current.phase != nil && stage.ID == p.current.phase.ID() {
			attrs["request_phase"] = stage.Name
		}
		switch stage.Name {
		case requestPhaseWriteRequest:
			attrs["connection_reused"], _ = timeline.AttributeValue[bool](stage.Attributes, "connection_reused")
			attrs["got_connection_ms"] = stage.StartedAt.Sub(request.StartedAt).Milliseconds()
		case requestPhaseAwaitResponse:
			attrs["request_written"] = true
			attrs["request_written_ms"] = stage.StartedAt.Sub(request.StartedAt).Milliseconds()
		case requestPhaseResponseRead:
			attrs["response_started"] = true
			attrs["response_started_ms"] = stage.StartedAt.Sub(request.StartedAt).Milliseconds()
		}
	}
	return attrs
}
