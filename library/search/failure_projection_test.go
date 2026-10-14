package search

import (
	"context"
	"strings"
	"testing"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// TestFetchLogErrorMeaningfulSanitizedCause keeps useful text while excluding labeled payloads.
func TestFetchLogErrorMeaningfulSanitizedCause(t *testing.T) {
	for _, tc := range []struct{ cause, useful string }{
		{"navigation failed: unexpected EOF", "unexpected EOF"},
		{"request failed api_key=private-key-canary; HTTP 429", "HTTP 429"},
		{"GET https://private-host-canary/private-path?token=private-url-canary failed: timeout", "timeout"},
		{"conversion failed response body: private-page-canary", "conversion failed"},
		{"worker failed payload={password:private-body-canary}", "worker failed"},
		{strings.Repeat("x", fetchDiagnosticTextLimit+1), "[diagnostic text exceeds limit]"},
	} {
		cause := errors.New(tc.cause)
		original := fetchFailure(cause, failureCodeCrawler, failureStageRender, "")
		projection := FetchLogError(original)
		require.ErrorIs(t, original, cause, "original error identity remains intact")
		require.NotErrorIs(t, projection, cause, "presentation cannot unwrap to raw content")
		require.NotContains(t, projection.Error(), "canary")
		require.Contains(t, projection.Error(), tc.useful)
		require.Contains(t, projection.Error(), "crawler_failed: render")
	}
	require.Nil(t, FetchLogError(nil))
	cause := context.Canceled
	original := fetchFailure(cause, "crawler_result_failed", "wait", "")
	require.ErrorIs(t, original, context.Canceled)
	require.Contains(t, FetchLogError(original).Error(), "context canceled")
}

type fetchPanicCause struct{}

func (fetchPanicCause) Error() string { panic("private-panic-canary") }

func TestFetchLogErrorDoesNotExposePanicDetails(t *testing.T) {
	projection := FetchLogError(fetchPanicCause{})
	require.Contains(t, projection.Error(), "[error text unavailable]")
	require.NotContains(t, projection.Error(), "canary")
}
