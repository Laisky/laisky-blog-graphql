// Package httpbody enforces per-route request-body budgets: a byte cap and a
// read deadline that only covers the time spent reading the body.
//
// A server-wide http.Server.ReadTimeout is deliberately not used: it applies
// one budget to every route and also covers header reading. Verified against
// Go 1.27 net/http: connReader.startBackgroundRead clears the connection read
// deadline when the post-body background read starts, so an expired deadline
// does not cancel a long-lived handler (SSE stream, held long poll) through
// that read; a deadline armed after that point would, because a timed-out
// background read calls conn.cancelCtx. Budgets here are therefore armed only
// for requests that carry a body, before any byte is read, and released again
// at EOF, so they never depend on that ordering.
package httpbody

import (
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// Budget bounds one request body.
type Budget struct {
	// MaxBytes caps the body; zero or negative leaves the cap to the route.
	MaxBytes int64
	// ReadTimeout bounds the time spent reading the body; zero disables it.
	ReadTimeout time.Duration
}

// TooLargeError reports a body above the budget.
type TooLargeError struct {
	// Limit is the byte cap that was exceeded.
	Limit int64
}

// Error describes the exceeded budget.
func (e *TooLargeError) Error() string {
	return "request body exceeds the configured limit"
}

// Result describes how a budget was installed on a request.
type Result struct {
	// DeadlineArmed reports whether a read deadline now covers the body.
	DeadlineArmed bool
	// DeadlineErr is why a configured deadline could not be armed, e.g. a
	// ResponseWriter chain without Unwrap support.
	DeadlineErr error
}

// Enforce installs budget on r before any handler reads the body. It returns a
// *TooLargeError, without reading anything, when the declared Content-Length
// already exceeds the cap; the caller then writes 413. Requests without a body
// get no deadline, so streams and long polls are never cut by it.
func Enforce(w http.ResponseWriter, r *http.Request, budget Budget) (Result, error) {
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return Result{}, nil
	}
	if budget.MaxBytes > 0 && r.ContentLength > budget.MaxBytes {
		return Result{}, errors.WithStack(&TooLargeError{Limit: budget.MaxBytes})
	}

	body := r.Body
	if budget.MaxBytes > 0 {
		body = http.MaxBytesReader(w, body, budget.MaxBytes)
	}

	var result Result
	if budget.ReadTimeout > 0 {
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(time.Now().Add(budget.ReadTimeout)); err != nil {
			result.DeadlineErr = errors.Wrap(err, "arm request body read deadline")
		} else {
			result.DeadlineArmed = true
			body = &deadlineBody{ReadCloser: body, controller: controller}
		}
	}
	r.Body = body
	return result, nil
}

// Tighten lowers the byte cap of an already budgeted request, e.g. for an
// unauthenticated caller. It returns a *TooLargeError when the declared
// Content-Length exceeds the new cap.
func Tighten(w http.ResponseWriter, r *http.Request, maxBytes int64) error {
	if r == nil || r.Body == nil || r.Body == http.NoBody || maxBytes <= 0 {
		return nil
	}
	if r.ContentLength > maxBytes {
		return errors.WithStack(&TooLargeError{Limit: maxBytes})
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	return nil
}

// IsTooLarge reports whether err means the body exceeded its byte budget.
func IsTooLarge(err error) bool {
	var declared *TooLargeError
	var streamed *http.MaxBytesError
	return errors.As(err, &declared) || errors.As(err, &streamed)
}

// IsTimeout reports whether err means the body read deadline expired.
func IsTimeout(err error) bool {
	return errors.Is(err, os.ErrDeadlineExceeded)
}

// Limit returns the byte cap carried by a too-large error, or zero.
func Limit(err error) int64 {
	var declared *TooLargeError
	if errors.As(err, &declared) {
		return declared.Limit
	}
	var streamed *http.MaxBytesError
	if errors.As(err, &streamed) {
		return streamed.Limit
	}
	return 0
}

// deadlineBody clears the connection read deadline once the body is consumed,
// before the server's background read could be cut by it.
type deadlineBody struct {
	io.ReadCloser
	controller *http.ResponseController
	once       sync.Once
}

// Read forwards to the body and releases the deadline at EOF. Other errors keep
// the deadline, so any server-side drain of an abandoned body stays bounded.
func (b *deadlineBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if errors.Is(err, io.EOF) {
		b.release()
	}
	return n, err //nolint:wrapcheck // io.Reader contract: io.EOF must be returned unwrapped
}

// release clears the read deadline exactly once.
func (b *deadlineBody) release() {
	b.once.Do(func() {
		_ = b.controller.SetReadDeadline(time.Time{})
	})
}
