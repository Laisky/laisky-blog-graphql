// Package crawlercapture writes private, complete failure logs for offline reproduction.
package crawlercapture

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
)

const (
	capturedState   = "captured"
	notReachedState = "not_reached"
)

// Blob is a byte-preserving value. Captured zero bytes differs from not_reached.
// JSON encodes Data as base64; strings, Unicode and invalid bytes are never repaired.
type Blob struct {
	State      string `json:"state"`
	ByteLength int64  `json:"byte_length"`
	SHA256     string `json:"sha256"`
	Data       []byte `json:"base64"`
	text       *string
	owned      []byte
}

// Captured snapshots an observed value, including an explicitly observed empty value.
func Captured(data []byte) Blob {
	cloned := make([]byte, len(data))
	copy(cloned, data)
	return observedBlob(cloned)
}

// CapturedString snapshots exact string bytes without UTF-8 normalization.
func CapturedString(data string) Blob {
	hash := sha256.New()
	_, _ = io.Copy(hash, readerOnly{strings.NewReader(data)})
	return Blob{State: capturedState, ByteLength: int64(len(data)), SHA256: hex.EncodeToString(hash.Sum(nil)), text: &data}
}

// observedBlob describes bytes already independently owned by the capture.
func observedBlob(data []byte) Blob {
	sum := sha256.Sum256(data)
	return Blob{State: capturedState, ByteLength: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), owned: data}
}

// NotReached records that an operation did not provide a value.
func NotReached() Blob { return Blob{State: notReachedState} }

// Validate checks value completeness and byte identity without interpreting its contents.
func (b Blob) Validate() error {
	switch b.State {
	case notReachedState:
		if b.ByteLength != 0 || b.SHA256 != "" || b.hasContent() {
			return errors.New("invalid absent capture value")
		}
	case capturedState:
		hash := sha256.New()
		n, err := io.Copy(hash, readerOnly{b.reader()})
		if err != nil || !b.hasContent() || b.ByteLength != n || !sameDigest(b.SHA256, hex.EncodeToString(hash.Sum(nil))) {
			return errors.New("capture value integrity failure")
		}
	default:
		return errors.New("unknown capture value state")
	}
	return nil
}

// Source identifies the application process that authored the capture.
type Source struct {
	Service       string `json:"service"`
	Hostname      string `json:"hostname"`
	ProcessRunID  string `json:"process_run_id"`
	Revision      string `json:"revision,omitempty"`
	Modified      bool   `json:"modified"`
	GoVersion     string `json:"go_version,omitempty"`
	ModuleVersion string `json:"module_version,omitempty"`
}

// Correlation joins caller and worker logs without altering queue or billing state.
type Correlation struct {
	TaskID    string `json:"task_id,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// Failure retains the app-authored classification, never an arbitrary error object.
type Failure struct {
	Stage       string `json:"stage"`
	Code        string `json:"code"`
	WorkerStage string `json:"worker_stage,omitempty"`
	WorkerCode  string `json:"worker_code,omitempty"`
}

// Request contains only declared crawler inputs, excluding credentials and unknown arguments.
type Request struct {
	OriginalURL            Blob  `json:"original_url"`
	SubmittedURL           Blob  `json:"submitted_url"`
	OutputMarkdown         bool  `json:"output_markdown"`
	OutputMarkdownDeclared bool  `json:"output_markdown_declared"`
	OriginalOutputMarkdown Blob  `json:"original_output_markdown"`
	DeclaredOutputMarkdown *bool `json:"declared_output_markdown,omitempty"`
}

// Converter records the exact argument passed to the named pinned converter.
type Converter struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	Input   Blob   `json:"input"`
}

// Renderer retains a failed pinned exchange without arbitrary headers.
// EncodedBodyBytes counts content-encoded response.Body bytes, not TCP framing.
type Renderer struct {
	PeerAddress           string   `json:"peer_address,omitempty"`
	Protocol              string   `json:"protocol,omitempty"`
	RequestID             string   `json:"request_id"`
	ParentRequestID       string   `json:"parent_request_id,omitempty"`
	Kind                  string   `json:"kind"`
	Method                string   `json:"method"`
	URL                   Blob     `json:"url"`
	UpstreamStatus        int      `json:"upstream_status"`
	DeclaredContentLength int64    `json:"declared_content_length"`
	ContentEncoding       string   `json:"content_encoding,omitempty"`
	TransferEncoding      []string `json:"transfer_encoding,omitempty"`
	EncodedBodyBytes      int64    `json:"encoded_body_bytes"`
	DecodedBody           Blob     `json:"decoded_body"`
	DecodedBodyComplete   bool     `json:"decoded_body_complete"`
	FailurePhase          string   `json:"failure_phase"`
	DecodeError           string   `json:"decode_error,omitempty"`
	DecodeCloseError      string   `json:"decode_close_error,omitempty"`
	ReadError             string   `json:"read_error,omitempty"`
	CloseError            string   `json:"close_error,omitempty"`
}

// Record is a complete private log envelope. Journal.Write owns identity and seal.
type Record struct {
	Version        int         `json:"version"`
	CaptureID      string      `json:"capture_id"`
	CreatedAt      time.Time   `json:"created_at"`
	Complete       bool        `json:"complete"`
	Source         Source      `json:"source"`
	Correlation    Correlation `json:"correlation"`
	Failure        Failure     `json:"failure"`
	Request        Request     `json:"request"`
	Converter      Converter   `json:"converter"`
	RenderedHTML   Blob        `json:"rendered_html"`
	Renderer       *Renderer   `json:"renderer,omitempty"`
	Retention      string      `json:"retention"`
	ManifestSHA256 string      `json:"manifest_sha256"`
}

// blobs returns the fixed explicit set of allowed captured input values.
func (r *Record) blobs() []*Blob {
	values := []*Blob{&r.Request.OriginalURL, &r.Request.SubmittedURL, &r.Request.OriginalOutputMarkdown, &r.Converter.Input, &r.RenderedHTML}
	if r.Renderer != nil {
		values = append(values, &r.Renderer.URL, &r.Renderer.DecodedBody)
	}
	return values
}

// manifestDigest binds all metadata and each payload's length/hash without duplicating payload data.
func (r Record) manifestDigest() (string, error) {
	r.ManifestSHA256 = ""
	if r.Renderer != nil {
		cloned := *r.Renderer
		r.Renderer = &cloned
	}
	for _, b := range r.blobs() {
		b.Data = nil
		b.owned = nil
		b.text = nil
	}
	data, err := json.Marshal(r)
	if err != nil {
		return "", errors.Wrap(err, "encode capture manifest")
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Validate rejects incomplete envelopes, missing values and any corrupted payload.
func (r Record) Validate() error {
	if r.Version != 1 || !r.Complete || r.CaptureID == "" || r.CreatedAt.IsZero() || r.Source.Service == "" || r.Source.Hostname == "" || r.Source.ProcessRunID == "" || r.Failure.Stage == "" || r.Failure.Code == "" {
		return errors.New("incomplete capture manifest")
	}
	for _, b := range r.blobs() {
		if err := b.Validate(); err != nil {
			return err
		}
	}
	if r.Request.OutputMarkdownDeclared != (r.Request.OriginalOutputMarkdown.State == capturedState) {
		return errors.New("capture option presence mismatch")
	}
	digest, err := r.manifestDigest()
	if err != nil {
		return err
	}
	if !sameDigest(digest, r.ManifestSHA256) {
		return errors.New("capture manifest integrity failure")
	}
	return nil
}

// Decode reads exactly one sealed envelope and validates its complete contents.
func Decode(reader io.Reader) (Record, error) {
	var record Record
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return Record{}, errors.Wrap(err, "decode failure capture")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Record{}, errors.New("capture has trailing data")
	}
	if err := record.Validate(); err != nil {
		return Record{}, err
	}
	return record, nil
}

// SameInput reports byte identity for an observed value without string normalization.
func SameInput(a, b Blob) bool {
	return a.State == b.State && a.ByteLength == b.ByteLength && sameDigest(a.SHA256, b.SHA256) && bytes.Equal(a.Bytes(), b.Bytes())
}

// sameDigest compares fixed-length hexadecimal digest text without timing-sensitive equality.
func sameDigest(a, b string) bool {
	return len(a) == 64 && len(b) == 64 && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// hasContent distinguishes a captured empty source from a missing payload.
func (b Blob) hasContent() bool { return b.Data != nil || b.owned != nil || b.text != nil }

// reader exposes immutable source bytes only to codec internals.
func (b Blob) reader() io.Reader {
	if b.text != nil {
		return strings.NewReader(*b.text)
	}
	if b.owned != nil {
		return bytes.NewReader(b.owned)
	}
	return bytes.NewReader(b.Data)
}

// Bytes returns a caller-owned reconstruction; journal writing itself does not allocate this copy.
func (b Blob) Bytes() []byte {
	if b.text != nil {
		return []byte(*b.text)
	}
	data := b.Data
	if b.owned != nil {
		data = b.owned
	}
	cloned := make([]byte, len(data))
	copy(cloned, data)
	return cloned
}

// readerOnly prevents io.Copy fast paths from converting an entire immutable string to bytes.
type readerOnly struct{ io.Reader }

// Clone preserves immutable captured sources and snapshots publicly decoded bytes.
func (b Blob) Clone() Blob {
	if b.Data != nil {
		b.Data = bytes.Clone(b.Data)
	}
	return b
}
