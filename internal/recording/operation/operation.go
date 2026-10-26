// Package operation records CCR-owned work and parentage. Call bodies belong to
// native AgentGo Sessions; JobView is the public observation projection.
package operation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/compforge/go-stdx/uuid"
)

type ID string
type Status string

const (
	Unknown   Status = "unknown"
	Running   Status = "running"
	Succeeded Status = "completed"
	Failed    Status = "failed"
	Canceled  Status = "cancelled"
)

type Attribute struct {
	Key   string
	Value any
}
type Record struct {
	ID         ID                         `json:"id"`
	ParentID   ID                         `json:"parent_id,omitempty"`
	Name       string                     `json:"name"`
	StartedAt  time.Time                  `json:"started_at"`
	FinishedAt time.Time                  `json:"finished_at,omitzero"`
	Status     Status                     `json:"status"`
	Error      string                     `json:"error,omitempty"`
	Attributes map[string]json.RawMessage `json:"attributes,omitempty"`
}

func (r Record) Duration(at time.Time) time.Duration {
	end := r.FinishedAt
	if end.IsZero() {
		end = at
	}
	if end.IsZero() || end.Before(r.StartedAt) {
		return 0
	}
	return end.Sub(r.StartedAt)
}

// Snapshot is the in-memory index of the append-only business journal.
// Only changed Records are persisted, never this aggregate.
type Snapshot struct {
	positions  map[ID]int
	Error      string    `json:"error,omitempty"`
	CapturedAt time.Time `json:"-"`
	ID         string    `json:"id"`
	Operation  string    `json:"operation"`
	RootID     ID        `json:"root_id"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	Status     Status    `json:"status"`
	Operations []Record  `json:"operations"`
}
type Ref struct {
	ReviewID    string
	OperationID ID
}
type contextKey struct{}

func NewContext(ctx context.Context, ref Ref) context.Context {
	return context.WithValue(ctx, contextKey{}, ref)
}
func FromContext(ctx context.Context) (Ref, bool) {
	v, ok := ctx.Value(contextKey{}).(Ref)
	return v, ok
}

type Option func(*Record)
type EndOption = Option

func WithID(id ID) Option     { return func(r *Record) { r.ID = id } }
func WithParent(id ID) Option { return func(r *Record) { r.ParentID = id } }
func WithStartTime(at time.Time) Option {
	return func(r *Record) {
		if !at.IsZero() {
			r.StartedAt = at
		}
	}
}
func WithEndTime(at time.Time) EndOption {
	return func(r *Record) {
		if !at.IsZero() {
			r.FinishedAt = at
		}
	}
}
func WithAttributes(attrs ...Attribute) Option {
	return func(r *Record) {
		if r.Attributes == nil {
			r.Attributes = map[string]json.RawMessage{}
		}
		for _, a := range attrs {
			b, e := json.Marshal(a.Value)
			if e == nil {
				r.Attributes[a.Key] = b
			}
		}
	}
}
func WithEndAttributes(attrs ...Attribute) EndOption { return WithAttributes(attrs...) }
func AttributeValue[T any](attrs map[string]json.RawMessage, key string) (T, bool) {
	var v T
	b, ok := attrs[key]
	if !ok {
		return v, false
	}
	err := json.Unmarshal(b, &v)
	return v, err == nil
}

// Sink appends one operation state to its Review's existing JSONL writer.
type Sink func(Record) error
type journal struct {
	mu      sync.Mutex
	root    Record
	records map[ID]Record
	order   []ID
	sink    Sink
	err     error
}

var journals sync.Map
var ErrNotFound = errors.New("review operation journal not found")

func Start(id, name string, sinks ...Sink) error {
	j := &journal{root: Record{ID: ID("operation:" + id), Name: name, StartedAt: time.Now(), Status: Running}, records: map[ID]Record{}}
	if len(sinks) > 0 {
		j.sink = sinks[0]
	}
	if _, loaded := journals.LoadOrStore(id, j); loaded {
		return fmt.Errorf("review %s already recording", id)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.write(j.root)
}
func (j *journal) write(r Record) error {
	if j.err != nil {
		return j.err
	}
	if j.sink != nil {
		j.err = j.sink(r)
	}
	return j.err
}
func Release(id string) { journals.Delete(id) }
func Finish(id string, err error) error {
	v, ok := journals.Load(id)
	if !ok {
		return ErrNotFound
	}
	j := v.(*journal)
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.root.FinishedAt.IsZero() {
		return j.err
	}
	j.root.FinishedAt = time.Now()
	j.root.Status = status(err)
	if err != nil {
		j.root.Error = err.Error()
	}
	return j.write(j.root)
}
func status(err error) Status {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Canceled
	}
	if err != nil {
		return Failed
	}
	return Succeeded
}

type Handle struct {
	j  *journal
	id ID
}

func (h Handle) ID() ID { return h.id }
func Begin(id, name string, opts ...Option) (Handle, error) {
	v, ok := journals.Load(id)
	if !ok {
		return Handle{}, ErrNotFound
	}
	j := v.(*journal)
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.root.FinishedAt.IsZero() {
		return Handle{}, fmt.Errorf("review %s is closed", id)
	}
	r := Record{ID: ID(uuid.V4()), Name: name, StartedAt: time.Now(), Status: Running}
	for _, f := range opts {
		f(&r)
	}
	if _, exists := j.records[r.ID]; exists {
		return Handle{}, fmt.Errorf("duplicate operation %s in review %s", r.ID, id)
	}
	j.records[r.ID] = r
	j.order = append(j.order, r.ID)
	return Handle{j: j, id: r.ID}, j.write(r)
}

// End persists cancellation too. The caller owns the operation result; children
// left open by an interrupted producer remain open as evidence of the gap.
func (h Handle) End(err error, opts ...EndOption) error {
	if h.j == nil {
		return nil
	}
	j := h.j
	j.mu.Lock()
	defer j.mu.Unlock()
	r := j.records[h.id]
	if !r.FinishedAt.IsZero() {
		return j.err
	}
	r.FinishedAt = time.Now()
	r.Status = status(err)
	if err != nil {
		r.Error = err.Error()
	}
	for _, f := range opts {
		f(&r)
	}
	j.records[h.id] = r
	return j.write(r)
}
func Read(id string) (Snapshot, error) {
	v, ok := journals.Load(id)
	if !ok {
		return Snapshot{}, ErrNotFound
	}
	j := v.(*journal)
	j.mu.Lock()
	defer j.mu.Unlock()
	s := Snapshot{CapturedAt: time.Now()}
	s.Apply(id, j.root)
	for _, key := range j.order {
		s.Apply(id, j.records[key])
	}
	return s, j.err
}

// Apply folds full states in file order. Parentage comes only from ParentID.
func (s *Snapshot) Apply(reviewID string, r Record) {
	s.ID = reviewID
	if r.ID == ID("operation:"+reviewID) {
		s.Error = r.Error
		s.RootID = r.ID
		s.Operation = r.Name
		s.StartedAt = r.StartedAt
		s.FinishedAt = r.FinishedAt
		s.Status = r.Status
		if r.FinishedAt.IsZero() {
			s.Status = Unknown
		}
		return
	}
	r.Attributes = cloneAttributes(r.Attributes)
	if s.positions == nil {
		s.positions = map[ID]int{}
		for i, op := range s.Operations {
			s.positions[op.ID] = i
		}
	}
	if i, exists := s.positions[r.ID]; exists {
		s.Operations[i] = r
		return
	}
	s.positions[r.ID] = len(s.Operations)
	s.Operations = append(s.Operations, r)
}
func cloneAttributes(attrs map[string]json.RawMessage) map[string]json.RawMessage {
	if attrs == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(attrs))
	for k, v := range attrs {
		out[k] = append(json.RawMessage(nil), v...)
	}
	return out
}
