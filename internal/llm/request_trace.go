package llm

import (
	"io"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"
)

const (
	requestPhaseUnknown        = "unknown"
	requestPhaseConnectionPool = "connection_pool"
	requestPhaseConnect        = "connect"
	requestPhaseWriteRequest   = "request_write"
	requestPhaseAwaitResponse  = "await_response"
	requestPhaseResponseRead   = "response_read"
)

// requestTrace retains the latest transport phase for error diagnostics only.
// The business timeline owns model calls, not HTTP spans.
type requestTrace struct {
	current  *httpAttempt
	attempts int
}

type httpAttempt struct {
	mu      sync.Mutex
	started time.Time
	name    string
	facts   map[string]any
	done    bool
}

func newRequestTrace() *requestTrace { return &requestTrace{} }

func (p *requestTrace) middleware(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	p.attempts++
	a := &httpAttempt{started: time.Now(), facts: map[string]any{}}
	p.current = a
	a.transition(requestPhaseConnectionPool)
	trace := &httptrace.ClientTrace{
		GetConn:      func(string) { a.transition(requestPhaseConnectionPool) },
		DNSStart:     func(httptrace.DNSStartInfo) { a.transition(requestPhaseConnect) },
		ConnectStart: func(string, string) { a.transition(requestPhaseConnect) },
		GotConn: func(info httptrace.GotConnInfo) {
			a.connected(info.Reused)
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err != nil {
				a.finish()
				return
			}
			a.wroteRequest()
		},
		GotFirstResponseByte: func() { a.transition(requestPhaseResponseRead) },
	}
	resp, err := next(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
	if err != nil {
		a.finish()
		return resp, err
	}
	if resp == nil {
		return resp, err
	}
	a.transition(requestPhaseResponseRead)
	if resp.Body == nil {
		a.finish()
	} else {
		// RoundTrip returns at headers. Keep response_read open until body EOF,
		// read failure or Close, including failed responses retried by the SDK.
		resp.Body = &tracedBody{ReadCloser: resp.Body, attempt: a}
	}
	return resp, err
}

func (a *httpAttempt) transition(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// A late write/connect callback cannot overwrite an observed response.
	if a.done || a.name == name || a.name == requestPhaseResponseRead {
		return
	}
	a.name = name
	elapsed := time.Since(a.started).Milliseconds()
	switch name {
	case requestPhaseWriteRequest:
		a.facts["got_connection_ms"] = elapsed
	case requestPhaseAwaitResponse:
		a.facts["request_written"] = true
		a.facts["request_written_ms"] = elapsed
	case requestPhaseResponseRead:
		a.facts["response_started"] = true
		a.facts["response_started_ms"] = elapsed
	}
}

func (a *httpAttempt) connected(reused bool) {
	a.mu.Lock()
	a.facts["connection_reused"] = reused
	a.mu.Unlock()
	a.transition(requestPhaseWriteRequest)
}

func (a *httpAttempt) wroteRequest() {
	a.mu.Lock()
	a.facts["request_written"] = true
	a.mu.Unlock()
	a.transition(requestPhaseAwaitResponse)
}

func (a *httpAttempt) finish() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.done = true
}

func (p *requestTrace) finish() {
	if p.current != nil {
		p.current.finish()
	}
}

type tracedBody struct {
	io.ReadCloser
	attempt *httpAttempt
}

func (b *tracedBody) Read(buf []byte) (int, error) {
	n, err := b.ReadCloser.Read(buf)
	if err != nil {
		b.attempt.finish()
	}
	return n, err
}

func (b *tracedBody) Close() error {
	err := b.ReadCloser.Close()
	b.attempt.finish()
	return err
}

// attributes projects the final HTTP attempt for the stable failure taxonomy.
func (p *requestTrace) attributes() map[string]any {
	attrs := map[string]any{"request_phase": requestPhaseUnknown, "http_attempts": p.attempts, "connection_reused": false, "request_written": false, "response_started": false}
	if p.current == nil {
		return attrs
	}
	p.current.mu.Lock()
	defer p.current.mu.Unlock()
	if p.current.name != "" {
		attrs["request_phase"] = p.current.name
	}
	for key, value := range p.current.facts {
		attrs[key] = value
	}
	return attrs
}
