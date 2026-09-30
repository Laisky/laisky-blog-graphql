package mcp

import (
	"bytes"
	"io"
	"net/http"

	errors "github.com/Laisky/errors/v2"
)

// readAndRestoreRequestBody inspects at most the logging budget plus one byte.
// Replay preserves the complete downstream body and any partial-read failure;
// the actual consumer retains ownership of Close and its error.
func readAndRestoreRequestBody(r *http.Request, limit int) (string, bool, error) {
	if r.Body == nil {
		return "", false, nil
	}
	limit = max(0, min(limit, httpLogBodyLimit))
	source := r.Body
	data, err := io.ReadAll(io.LimitReader(source, int64(limit)+1))
	r.Body = &loggingReplayBody{ReadCloser: source, prefix: bytes.NewReader(data), readErr: err}
	prefix, truncated := truncateForLog(data, limit)
	return prefix, truncated, errors.WithStack(err)
}

// loggingReplayBody restores inspected bytes before continuing the original
// reader. A terminal inspection error must reach the handler rather than being
// consumed by the logger and silently replaced with a successful EOF.
type loggingReplayBody struct {
	io.ReadCloser
	prefix  *bytes.Reader
	readErr error
}

// Read replays the inspected prefix and error before delegating to the source.
func (b *loggingReplayBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.prefix.Len() > 0 {
		return b.prefix.Read(p)
	}
	if b.readErr != nil {
		return 0, b.readErr
	}
	return b.ReadCloser.Read(p)
}
