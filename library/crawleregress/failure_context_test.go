package crawleregress

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCrawlerFailureContextBoundaries preserves every valid maximal scalar and
// observed overrun; metadata counts never weaken the separate response budgets.
func TestCrawlerFailureContextBoundaries(t *testing.T) {
	host := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	mime := strings.Repeat("a", 63) + "/" + strings.Repeat("b", 63)
	status, redirect, count := int64(599), int64(MaxRedirects), int64(math.MaxInt64)
	value := &FailureContext{TargetScheme: "https", TargetHostname: host, FinalScheme: "https", FinalHostname: host,
		ContentType: mime, UpstreamStatus: &status, RedirectCount: &redirect, Converter: "html-to-markdown/v2",
		RenderedHTMLBytes: &count, ConversionInputBytes: &count, ConversionOutputBytes: &count, ConversionOutputTrimmedBytes: &count, BodyTextRunes: &count, BodyTextNodes: &count}
	reviewed := ReviewedFailureContext(value)
	require.NotNil(t, reviewed)
	require.Equal(t, value, reviewed)
	raw, err := json.Marshal(reviewed)
	require.NoError(t, err)
	require.LessOrEqual(t, len(raw), 2048)
	count = 0
	require.Equal(t, int64(math.MaxInt64), *reviewed.ConversionInputBytes, "copy cannot alias worker DTO counts")
	for _, url := range []string{"https://example.net/path?secret=not-logged", "http://[2606:4700:4700::1111]:8080/private"} {
		scheme, host := TargetComponents(url)
		require.NotEmpty(t, scheme)
		require.NotEmpty(t, host)
		require.NotContains(t, host, "private")
	}
}

// TestCrawlerFailureContextInvalid rejects a malformed optional envelope as a
// whole; it is not an error override or evidence that a page body is acceptable.
func TestCrawlerFailureContextInvalid(t *testing.T) {
	negative, badStatus, badRedirect := int64(-1), int64(700), int64(MaxRedirects+1)
	for _, value := range []*FailureContext{
		{FinalScheme: "https"}, {FinalScheme: "https", FinalHostname: "example.net/private"},
		{ContentType: "text/html; charset=utf-8"}, {ContentType: strings.Repeat("x", 128)},
		{Converter: "private-freeform-converter"}, {UpstreamStatus: &badStatus}, {RedirectCount: &badRedirect},
		{ConversionInputBytes: &negative},
	} {
		require.Nil(t, ReviewedFailureContext(value))
	}
	require.Nil(t, ReviewedFailureContext(nil))
	zero := int64(0)
	value := ReviewedFailureContext(&FailureContext{ConversionOutputBytes: &zero})
	require.NotNil(t, value)
	require.NotNil(t, value.ConversionOutputBytes)
	require.Nil(t, value.UpstreamStatus)
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	require.Contains(t, string(raw), "conversion_output_bytes")
	require.NotContains(t, string(raw), "upstream_status")
	scheme, host := TargetComponents("https://user:password@example.net/private")
	require.Empty(t, scheme)
	require.Empty(t, host)
}
