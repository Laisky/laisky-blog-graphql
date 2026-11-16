package httpbody

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// TestNoteUnarmedDeadlineCountsAndSamples (maintainer review of PR #95, P3):
// when a read deadline cannot be armed, only the byte cap bounds that body,
// so every such request is counted per entry and callers WARN at most once
// per interval per entry, carrying how many they suppressed.
func TestNoteUnarmedDeadlineCountsAndSamples(t *testing.T) {
	sampler := NewUnarmedReporter(time.Minute)
	start := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	before := testutil.ToFloat64(unarmedDeadlines.WithLabelValues("test_entry"))

	warn, suppressed := sampler.Record("test_entry", start)
	require.True(t, warn)
	require.Zero(t, suppressed)
	for i := range 3 {
		warn, _ = sampler.Record("test_entry", start.Add(time.Duration(i+1)*time.Second))
		require.False(t, warn, "one WARN per interval")
	}
	warn, _ = sampler.Record("other_entry", start.Add(time.Second))
	require.True(t, warn, "entries are sampled independently")
	warn, suppressed = sampler.Record("test_entry", start.Add(time.Minute))
	require.True(t, warn)
	require.EqualValues(t, 3, suppressed)

	require.InDelta(t, 5, testutil.ToFloat64(unarmedDeadlines.WithLabelValues("test_entry"))-before, 0,
		"every unarmed request is counted, sampled or not")
}

// TestEnforceReportsAnUnarmableDeadline: a ResponseWriter that cannot set a
// read deadline (here httptest's recorder) leaves the body bounded by its
// byte cap only, and Enforce says so instead of failing the request.
func TestEnforceReportsAnUnarmableDeadline(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("body"))
	result, err := Enforce(httptest.NewRecorder(), request, Budget{MaxBytes: 1 << 10, MinBytesPerSecond: 1024, Grace: time.Second})
	require.NoError(t, err)
	require.False(t, result.DeadlineArmed)
	require.ErrorIs(t, result.DeadlineErr, http.ErrNotSupported)
}
