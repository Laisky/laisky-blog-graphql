package plugin

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
	"time"

	errors "github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// SearchRecord is one paired live/shadow search captured by ShadowPlugin.
type SearchRecord struct {
	Timestamp      time.Time          `json:"timestamp"`
	Project        string             `json:"project"`
	Query          string             `json:"query"`
	PathPrefix     string             `json:"path_prefix"`
	Limit          int                `json:"limit"`
	LiveResult     files.SearchResult `json:"live_result"`
	ShadowResult   files.SearchResult `json:"shadow_result"`
	LiveDuration   time.Duration      `json:"live_duration_ns"`
	ShadowDuration time.Duration      `json:"shadow_duration_ns"`
	LiveErr        string             `json:"live_err,omitempty"`
	ShadowErr      string             `json:"shadow_err,omitempty"`
	LivePlugin     string             `json:"live_plugin"`
	ShadowPlugin   string             `json:"shadow_plugin"`
}

// MutationRecord is one paired live/shadow mutation captured by ShadowPlugin.
type MutationRecord struct {
	Timestamp      time.Time     `json:"timestamp"`
	Op             string        `json:"op"`
	Project        string        `json:"project"`
	Path           string        `json:"path"`
	LiveErr        string        `json:"live_err,omitempty"`
	ShadowErr      string        `json:"shadow_err,omitempty"`
	LiveDuration   time.Duration `json:"live_duration_ns"`
	ShadowDuration time.Duration `json:"shadow_duration_ns"`
}

// Recorder is the destination for paired live/shadow observations.
type Recorder interface {
	RecordSearch(rec SearchRecord) error
	RecordMutation(rec MutationRecord) error
	Close() error
}

// jsonlEnvelope tags each line so the file is self-describing.
type jsonlEnvelope struct {
	Kind     string          `json:"kind"`
	Search   *SearchRecord   `json:"search,omitempty"`
	Mutation *MutationRecord `json:"mutation,omitempty"`
}

// JSONLRecorder appends paired records to a JSONL file under a mutex.
type JSONLRecorder struct {
	mu     sync.Mutex
	file   *os.File
	writer *bufio.Writer
	closed bool
}

// NewJSONLRecorder opens path in append mode and returns a writer.
func NewJSONLRecorder(path string) (*JSONLRecorder, error) {
	if path == "" {
		return nil, errors.New("recorder path is required")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, errors.Wrap(err, "open recorder file")
	}
	return &JSONLRecorder{
		file:   f,
		writer: bufio.NewWriter(f),
	}, nil
}

// RecordSearch writes one search envelope as a JSONL line.
func (r *JSONLRecorder) RecordSearch(rec SearchRecord) error {
	return r.writeEnvelope(jsonlEnvelope{Kind: "search", Search: &rec})
}

// RecordMutation writes one mutation envelope as a JSONL line.
func (r *JSONLRecorder) RecordMutation(rec MutationRecord) error {
	return r.writeEnvelope(jsonlEnvelope{Kind: "mutation", Mutation: &rec})
}

// Close flushes the buffer and closes the file.
func (r *JSONLRecorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if err := r.writer.Flush(); err != nil {
		_ = r.file.Close()
		return errors.Wrap(err, "flush recorder")
	}
	if err := r.file.Close(); err != nil {
		return errors.Wrap(err, "close recorder")
	}
	return nil
}

// Rotate is intentionally unimplemented in Phase 3 wave 1; operators rotate
// out-of-band until the wave-2 manager lands.
func (r *JSONLRecorder) Rotate() error {
	// TODO(phase3-wave2): implement size/time-based rotation per the operator manual.
	return errors.New("JSONLRecorder.Rotate is not implemented yet")
}

func (r *JSONLRecorder) writeEnvelope(env jsonlEnvelope) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("recorder is closed")
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return errors.Wrap(err, "marshal record")
	}
	if _, err := r.writer.Write(payload); err != nil {
		return errors.Wrap(err, "write record")
	}
	if err := r.writer.WriteByte('\n'); err != nil {
		return errors.Wrap(err, "write newline")
	}
	if err := r.writer.Flush(); err != nil {
		return errors.Wrap(err, "flush record")
	}
	return nil
}

// MemRecorder collects records in memory; useful in tests.
type MemRecorder struct {
	mu        sync.Mutex
	searches  []SearchRecord
	mutations []MutationRecord
}

// NewMemRecorder constructs an empty MemRecorder.
func NewMemRecorder() *MemRecorder { return &MemRecorder{} }

// RecordSearch appends to the in-memory search log.
func (m *MemRecorder) RecordSearch(rec SearchRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.searches = append(m.searches, rec)
	return nil
}

// RecordMutation appends to the in-memory mutation log.
func (m *MemRecorder) RecordMutation(rec MutationRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mutations = append(m.mutations, rec)
	return nil
}

// Records returns deep-copied slices of the captured records.
func (m *MemRecorder) Records() ([]SearchRecord, []MutationRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	searches := append([]SearchRecord(nil), m.searches...)
	mutations := append([]MutationRecord(nil), m.mutations...)
	return searches, mutations
}

// Close is a no-op for the in-memory recorder.
func (m *MemRecorder) Close() error { return nil }

// MultiRecorder fans every record out to N child recorders. The first non-nil
// child error is returned; others are still attempted so a slow file recorder
// does not silently drop the in-memory mirror used by tests.
type MultiRecorder struct {
	children []Recorder
}

// NewMultiRecorder fans out to all non-nil children.
func NewMultiRecorder(children ...Recorder) Recorder {
	out := make([]Recorder, 0, len(children))
	for _, c := range children {
		if c != nil {
			out = append(out, c)
		}
	}
	return &MultiRecorder{children: out}
}

// RecordSearch dispatches to every child.
func (m *MultiRecorder) RecordSearch(rec SearchRecord) error {
	var firstErr error
	for _, c := range m.children {
		if err := c.RecordSearch(rec); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// RecordMutation dispatches to every child.
func (m *MultiRecorder) RecordMutation(rec MutationRecord) error {
	var firstErr error
	for _, c := range m.children {
		if err := c.RecordMutation(rec); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Close closes every child; the first error wins.
func (m *MultiRecorder) Close() error {
	var firstErr error
	for _, c := range m.children {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
