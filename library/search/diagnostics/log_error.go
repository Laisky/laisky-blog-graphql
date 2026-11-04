package diagnostics

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"

	logSDK "github.com/Laisky/go-utils/v6/log"
)

const diagnosticCauseLimit = 2048
const diagnosticCauseNodes = 32
const unavailableCauseText = "[error text unavailable]"
const oversizedCauseText = "[diagnostic text exceeds limit]"

// LogError presents bounded useful cause text separately from the original error.
// It preserves the returned diagnostic and error tree, and never unwraps its own
// presentation to raw provider content, request addresses or verbose stacks.
func LogError(err error) error {
	if err == nil {
		return nil
	}
	return &diagnosticLogError{diagnostic: Summary(err), text: logCauseText(err)}
}

type diagnosticLogError struct {
	diagnostic Diagnostic
	text       string
}

func (e *diagnosticLogError) Error() string {
	prefix := "search " + e.diagnostic.Stage + " failed: " + e.diagnostic.Category
	if e.text == "" || e.text == prefix {
		return prefix
	}
	return prefix + ": " + e.text
}

type causeNode struct {
	cause         error
	stage         string
	sourceContext bool
}

func logCauseText(err error) (text string) {
	defer func() {
		if recover() != nil {
			text = unavailableCauseText
		}
	}()
	pending := []causeNode{{cause: err, stage: Summary(err).Stage}}
	parts := make([]string, 0, 4)
	count, size := 0, 0
	for len(pending) > 0 && count < diagnosticCauseNodes {
		node := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		count++
		if node.cause == nil {
			continue
		}

		children, expanded, overBudget := expandCauseNode(&node, diagnosticCauseNodes-count-len(pending))
		if overBudget {
			return "[additional diagnostic causes omitted]"
		}
		if expanded {
			pending = append(pending, children...)
			continue
		}

		message := leafCauseText(node)
		if message == "" {
			continue
		}
		size += len(message) + 2
		if size > diagnosticCauseLimit {
			return oversizedCauseText
		}
		parts = append(parts, message)
	}
	if len(pending) > 0 {
		parts = append(parts, "[additional diagnostic causes omitted]")
	}
	return strings.Join(parts, "; ")
}

func expandCauseNode(node *causeNode, remaining int) ([]causeNode, bool, bool) {
	if failure, ok := node.cause.(*FailureError); ok { //nolint:errorlint // Inspect this bounded node; errors.As would skip descendants and joined branches.
		if failure.cause == nil {
			return nil, false, false
		}
		if remaining < 1 {
			return nil, false, true
		}
		return []causeNode{{cause: failure.cause, stage: Normalize(failure.diagnostic).Stage}}, true, false
	}
	if _, reviewed := reviewedCauseText(node.cause); reviewed {
		return nil, false, false
	}
	if operation, ok := node.cause.(*net.OpError); ok && operation.Err != nil { //nolint:errorlint // Preserve this node's operation before walking its child within the budget.
		if remaining < 2 {
			return nil, false, true
		}
		contextText := logSDK.SanitizeDiagnosticText(strings.TrimSpace(operation.Op + " " + operation.Net))
		return []causeNode{
			{cause: operation.Err, stage: node.stage},
			{cause: operationContextError(contextText), stage: node.stage, sourceContext: true},
		}, true, false
	}
	if joined, ok := node.cause.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) > remaining {
			return nil, false, true
		}
		children := make([]causeNode, 0, len(causes))
		for i := len(causes) - 1; i >= 0; i-- {
			children = append(children, causeNode{cause: causes[i], stage: node.stage})
		}
		return children, true, false
	}
	wrapped, ok := node.cause.(interface{ Unwrap() error })
	if !ok {
		return nil, false, false
	}
	child := wrapped.Unwrap()
	if child == nil {
		return nil, false, false
	}
	// Provider source wrappers around content-free typed failures contain fixed
	// context, such as which configuration is missing, without a response body.
	if failure, ok := child.(*FailureError); ok && failure.cause == nil { //nolint:errorlint // Only the immediate child proves this wrapper has fixed source context.
		node.sourceContext = true
		return nil, false, false
	}
	if remaining < 1 {
		return nil, false, true
	}
	return []causeNode{{cause: child, stage: node.stage}}, true, false
}

func leafCauseText(node causeNode) string {
	if text, ok := reviewedCauseText(node.cause); ok {
		return logSDK.SanitizeDiagnosticText(text)
	}
	if node.sourceContext {
		return sanitizeSearchCauseText(node.cause.Error())
	}

	text := sanitizeSearchCauseText(node.cause.Error())
	if len(text) > diagnosticCauseLimit {
		return oversizedCauseText
	}
	return text
}

// sanitizeSearchCauseText redacts concrete parser payload tails while retaining
// the useful parser prefix. Other routine operational text uses SDK sanitization.
func sanitizeSearchCauseText(text string) string {
	for _, prefix := range []string{
		"malformed HTTP response",
		"malformed HTTP status code",
		"malformed HTTP version",
	} {
		if at := strings.Index(text, prefix+` "`); at >= 0 {
			text = text[:at] + prefix + ": [redacted response line]"
			break
		}
	}
	if at := strings.Index(text, "malformed MIME header line: "); at >= 0 {
		text = text[:at] + "malformed MIME header line: [redacted header line]"
	}
	return logSDK.SanitizeDiagnosticText(text)
}

var certificateInvalidReasons = map[x509.InvalidReason]string{
	x509.NotAuthorizedToSign:           "TLS certificate not authorized to sign certificates",
	x509.Expired:                       "TLS certificate expired or not yet valid",
	x509.CANotAuthorizedForThisName:    "TLS certificate violates name constraints",
	x509.TooManyIntermediates:          "TLS certificate chain exceeds path length",
	x509.IncompatibleUsage:             "TLS certificate incompatible key usage",
	x509.NameMismatch:                  "TLS certificate issuer name mismatch",
	x509.NameConstraintsWithoutSANs:    "TLS certificate missing required subject alternative names",
	x509.UnconstrainedName:             "TLS certificate name is unconstrained",
	x509.TooManyConstraints:            "TLS certificate exceeds constraint comparison limit",
	x509.CANotAuthorizedForExtKeyUsage: "TLS certificate authority disallows extended key usage",
	x509.NoValidChains:                 "TLS certificate has no valid verification chain",
}

type operationContextError string

func (e operationContextError) Error() string { return string(e) }

func reviewedCauseText(err error) (string, bool) {
	switch cause := err.(type) { //nolint:errorlint // The bounded walker visits descendants separately; errors.As would skip mixed causes.
	case *net.DNSError:
		return cause.Error(), true
	case x509.HostnameError:
		return "TLS hostname verification failed for " + logSDK.SanitizeDiagnosticText(cause.Host), true
	case x509.UnknownAuthorityError:
		return "TLS certificate signed by unknown authority", true
	case x509.CertificateInvalidError:
		if text, ok := certificateInvalidReasons[cause.Reason]; ok {
			return text, true
		}
		return "TLS certificate invalid", true
	case tls.RecordHeaderError:
		return "TLS record header rejected", true
	case *json.SyntaxError:
		return fmt.Sprintf("malformed JSON response at offset %d", cause.Offset), true
	case *json.UnmarshalTypeError:
		if cause.Type != nil {
			return "JSON response type mismatch: expected " + cause.Type.Kind().String(), true
		}
		return "JSON response type mismatch", true
	case syscall.Errno:
		return cause.Error(), true
	}
	switch err { //nolint:errorlint // Match only this node; errors.Is traverses unbounded descendants and can mask mixed causes.
	case context.Canceled:
		return "context canceled", true
	case context.DeadlineExceeded:
		return "context deadline exceeded", true
	case io.EOF:
		return "EOF", true
	case io.ErrUnexpectedEOF:
		return "unexpected EOF", true
	}
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() { //nolint:errorlint // Check this node after the bounded walker handles wrappers, not a descendant.
		return "provider request timed out", true
	}
	return "", false
}
