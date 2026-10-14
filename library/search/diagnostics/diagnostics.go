// Package diagnostics describes search failures without exporting request or provider content.
package diagnostics

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/url"
	"reflect"
	"syscall"

	"github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/google/uuid"
)

const (
	managerStage         = "manager"
	transportStage       = "transport"
	responseStage        = "response"
	decodeStage          = "decode"
	providerStage        = "provider"
	authCategory         = "auth"
	providerHTTPCategory = "provider_http"
	unknownValue         = "unknown"
	timeoutCategory      = "timeout"
	rateLimitCategory    = "rate_limit"
)

// Diagnostic is a bounded, content-free description of a search failure.
type Diagnostic struct {
	Stage, Category, Provider string
	Status, Attempts          int
}

// FailureError preserves an error chain while exposing only safe diagnostic text.
type FailureError struct {
	diagnostic Diagnostic
	cause      error
}

// Error returns a reviewed category rather than upstream error text.
func (e *FailureError) Error() string {
	return "search " + e.diagnostic.Stage + " failed: " + e.diagnostic.Category
}

// Unwrap preserves typed causes for errors.Is and errors.As.
func (e *FailureError) Unwrap() error { return e.cause }

// Normalize bounds all fields and replaces unreviewed values with honest defaults.
func Normalize(d Diagnostic) Diagnostic {
	switch d.Stage {
	case "configuration", "request", transportStage, responseStage, decodeStage, providerStage, managerStage, "encode":
	default:
		d.Stage = providerStage
	}
	switch d.Category {
	case "canceled", "deadline", "dns", "tls", "connection", timeoutCategory, rateLimitCategory, authCategory, providerHTTPCategory, "malformed_response", "internal", unknownValue:
	default:
		d.Category = unknownValue
	}
	switch d.Provider {
	case "google", "bing", "serp_google", "firecrawl":
	default:
		d.Provider = unknownValue
	}
	if d.Status < 100 || d.Status > 599 {
		d.Status = 0
	}
	if d.Category != authCategory && d.Category != rateLimitCategory && d.Category != providerHTTPCategory {
		d.Status = 0
	}
	if d.Attempts < 0 {
		d.Attempts = 0
	}
	if d.Attempts > 100 {
		d.Attempts = 100
	}
	return d
}

// New creates a safe typed failure at a known source stage.
func New(stage, category string) error {
	return &FailureError{diagnostic: Normalize(Diagnostic{Stage: stage, Category: category})}
}

// Wrap preserves a cause and classifies it by type, never by parsing its text.
func Wrap(err error, stage string) error {
	if err == nil {
		return nil
	}
	return wrapFailure(err, stage)
}

func wrapFailure(err error, stage string) *FailureError {
	// Parse/construction errors can also retain sensitive URLs. Drop that outer
	// wrapper without traversing and discarding a multi-provider error tree.
	var requestErr *url.Error
	if reflect.TypeOf(err) == reflect.TypeFor[*url.Error]() && errors.As(err, &requestErr) {
		err = requestErr.Err
	}
	d := Summary(err)
	d.Stage = stage
	var syntax *json.SyntaxError
	var shape *json.UnmarshalTypeError
	if stage == decodeStage && (errors.As(err, &syntax) || errors.As(err, &shape) || errors.Is(err, io.ErrUnexpectedEOF)) {
		d.Category = "malformed_response"
	}
	return &FailureError{diagnostic: Normalize(d), cause: err}
}

// HTTP records an upstream HTTP failure without its body or URL.
func HTTP(status int) error {
	category := providerHTTPCategory
	switch status {
	case 401, 403:
		category = authCategory
	case 429:
		category = rateLimitCategory
	}
	return &FailureError{diagnostic: Normalize(Diagnostic{Stage: responseStage, Category: category, Status: status})}
}

// WithProvider adds only a reviewed provider type, never an instance name or endpoint.
func WithProvider(err error, provider string) error {
	d := Summary(err)
	d.Provider = provider
	return &FailureError{diagnostic: Normalize(d), cause: err}
}

// WithAttempts records the number of engines actually invoked.
func WithAttempts(err error, attempts int) error {
	d := Summary(err)
	d.Attempts = attempts
	return &FailureError{diagnostic: Normalize(d), cause: err}
}

// Transport distinguishes provider-local timeouts from an ended caller context.
func Transport(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	failure := wrapFailure(err, transportStage)
	if ctx != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		failure.diagnostic.Category = timeoutCategory
	}
	return failure
}

// Stopped records caller termination while retaining every attempted failure cause.
func Stopped(cause error, failures []error, attempts int) error {
	d := Summary(Wrap(cause, managerStage))
	d.Attempts = attempts
	causes := append(append([]error{}, failures...), cause)
	return &FailureError{diagnostic: Normalize(d), cause: errors.Join(causes...)}
}

// Exhausted preserves every attempted cause. Mixed failures remain unknown at manager level.
func Exhausted(failures []error) error {
	d := Diagnostic{Stage: managerStage, Category: unknownValue, Attempts: len(failures)}
	if len(failures) > 0 {
		d = Summary(failures[0])
		d.Attempts = len(failures)
		for _, err := range failures[1:] {
			next := Summary(err)
			if next.Category != d.Category || next.Stage != d.Stage || next.Status != d.Status {
				d.Category, d.Stage, d.Status = unknownValue, managerStage, 0
			}
			if next.Provider != d.Provider {
				d.Provider = unknownValue
			}
		}
	}
	return &FailureError{diagnostic: Normalize(d), cause: errors.Join(failures...)}
}

// Summary returns safe metadata. Unknown errors are not guessed from their messages.
func Summary(err error) Diagnostic {
	d := Diagnostic{Stage: providerStage, Category: unknownValue}
	var failure *FailureError
	if errors.As(err, &failure) {
		return Normalize(failure.diagnostic)
	}
	switch {
	case errors.Is(err, context.Canceled):
		d.Category = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		d.Category = "deadline"
	default:
		var dns *net.DNSError
		var cert *tls.CertificateVerificationError
		var unknownAuthority x509.UnknownAuthorityError
		var hostname x509.HostnameError
		var invalidCert x509.CertificateInvalidError
		var record tls.RecordHeaderError
		var network net.Error
		switch {
		case errors.As(err, &dns):
			d.Category = "dns"
		case errors.As(err, &cert), errors.As(err, &unknownAuthority), errors.As(err, &hostname), errors.As(err, &invalidCert), errors.As(err, &record):
			d.Category = "tls"
		case errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE):
			d.Category = "connection"
		case errors.As(err, &network) && network.Timeout():
			d.Category = timeoutCategory
		}
	}
	return Normalize(d)
}

type requestIDKey struct{}

type loggerKey struct{}

// WithLogger binds an application base logger carrying only reviewed fields.
// Framework request loggers can carry user data and must not be supplied here.
func WithLogger(ctx context.Context, logger logSDK.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, logger)
}

// Logger selects the reviewed operation logger or the injected application base.
// It never inherits arbitrary fields from framework request loggers.
func Logger(ctx context.Context, fallback logSDK.Logger) logSDK.Logger {
	if ctx != nil {
		if logger, ok := ctx.Value(loggerKey{}).(logSDK.Logger); ok && logger != nil {
			return logger
		}
	}
	return fallback
}

// WithRequestID carries only a canonical nonzero UUID through the search path.
func WithRequestID(ctx context.Context, id string) context.Context {
	parsed, err := uuid.Parse(id)
	if err != nil || len(id) != 36 || parsed == uuid.Nil || parsed.String() != id {
		id = ""
	}
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the operation correlation identifier, if one was assigned.
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}
