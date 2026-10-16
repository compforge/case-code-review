// Package timelinelog persists one review timeline as append-only native updates.
package timelinelog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/compforge/go-stdx/timeline/store"
)

const Version = 1

type entry struct {
	Version int          `json:"version"`
	ID      string       `json:"id"`
	Seq     uint64       `json:"seq"`
	At      time.Time    `json:"at"`
	Update  store.Update `json:"update"`
}

type stream struct {
	file *os.File
	doc  store.Document
	seq  uint64
	at   time.Time
	err  error
}

// Store routes native updates to registered review files. Unregistered transport
// timelines remain ephemeral. The review lease owns every file's lifetime.
type Store struct {
	mu      sync.Mutex
	streams map[string]*stream
}

func New() *Store { return &Store{streams: make(map[string]*stream)} }

func (s *Store) Attach(id, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.streams[id]; ok {
		return fmt.Errorf("timeline %s already attached", id)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	s.streams[id] = &stream{file: f}
	return nil
}

func (s *Store) Detach(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.streams[id]
	if st == nil {
		return nil
	}
	delete(s.streams, id)
	return errors.Join(st.err, st.file.Sync(), st.file.Close())
}

// Merge appends only changed states, never a snapshot of previous stages.
// +spec=`A failed append is sticky; a retry must not append after a torn JSONL record`
func (s *Store) Merge(ctx context.Context, id string, update store.Update) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	st := s.streams[id]
	if st == nil {
		return nil
	}
	if st.err != nil {
		return st.err
	}
	doc, changed, err := store.MergeDocument(id, st.doc, update)
	if err != nil || !changed {
		return err
	}
	at := time.Now().UTC()
	raw, err := json.Marshal(entry{Version: Version, ID: id, Seq: st.seq + 1, At: at, Update: update})
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	n, err := st.file.Write(raw)
	if err == nil && n != len(raw) {
		err = io.ErrShortWrite
	}
	if err != nil {
		st.err = err
		return err
	}
	st.doc, st.seq, st.at = doc, st.seq+1, at
	return nil
}

func (s *Store) Read(ctx context.Context, id string) (store.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return store.Document{}, err
	}
	if st := s.streams[id]; st != nil && st.doc.ID != "" {
		return st.doc.Clone(), nil
	}
	return store.Document{}, store.ErrNotFound
}
func (s *Store) MGet(ctx context.Context, ids []string) ([]store.Document, error) {
	var docs []store.Document
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		d, err := s.Read(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	return docs, nil
}
func (s *Store) Latest(ctx context.Context, after time.Time, limit int) ([]store.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, nil
	}
	var streams []*stream
	for _, st := range s.streams {
		if st.at.After(after) {
			streams = append(streams, st)
		}
	}
	sort.Slice(streams, func(i, j int) bool {
		if streams[i].at.Equal(streams[j].at) {
			return streams[i].doc.ID > streams[j].doc.ID
		}
		return streams[i].at.After(streams[j].at)
	})
	var docs []store.Document
	for _, st := range streams[:min(limit, len(streams))] {
		docs = append(docs, st.doc.Clone())
	}
	return docs, nil
}

// Read replays a valid prefix. Only an unterminated final line may be discarded.
func Read(r io.Reader) (doc store.Document, captured time.Time, truncated bool, err error) {
	reader := bufio.NewReader(r)
	for seq := uint64(1); ; seq++ {
		line, e := reader.ReadBytes('\n')
		if e == io.EOF {
			return doc, captured, len(line) > 0, nil
		}
		if e != nil {
			return doc, captured, false, e
		}
		var record entry
		if e = json.Unmarshal(line, &record); e != nil {
			return doc, captured, false, fmt.Errorf("timeline line %d: %w", seq, e)
		}
		if record.Version != Version || record.Seq != seq || record.ID == "" || (doc.ID != "" && doc.ID != record.ID) {
			return doc, captured, false, fmt.Errorf("invalid timeline envelope at line %d", seq)
		}
		doc, _, e = store.MergeDocument(record.ID, doc, record.Update)
		if e != nil {
			return doc, captured, false, e
		}
		captured = record.At
	}
}
