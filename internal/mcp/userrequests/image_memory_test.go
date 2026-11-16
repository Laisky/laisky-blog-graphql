package userrequests

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/imageproc"
)

// defaultEnvelopeSettings returns the shipped image limits.
func defaultEnvelopeSettings() ImageSettings {
	return ImageSettings{
		PerImageMaxBytes:        DefaultImagePerImageMaxBytes,
		MaxPerRequest:           DefaultImageMaxPerRequest,
		MaxConcurrentCreates:    DefaultImageMaxConcurrentCreates,
		DecodeMemoryBudgetBytes: DefaultImageDecodeMemoryBudgetBytes,
	}
}

// TestImageMemoryEnvelopeDefaults pins the terms of the envelope the review
// asked to make explicit (PR #94, P2): the decode budget alone is not a
// process bound, because every running create also holds its multipart form,
// one raw attachment and every image it already prepared.
func TestImageMemoryEnvelopeDefaults(t *testing.T) {
	e := defaultEnvelopeSettings().MemoryEnvelope()
	require.Equal(t, DefaultImageDecodeMemoryBudgetBytes, e.DecodeBudget)
	require.Equal(t, int64(8*(18<<20+20<<20)), e.InputBuffers)
	require.Equal(t, 8*5*imageproc.MaxNormalizedPNGBytes, e.PreparedOutputs)
	require.Greater(t, e.Total(), int64(2200<<20), "the defaults need about 2.25 GiB")
	require.Less(t, e.Total(), int64(2400<<20))
	require.Equal(t, e, ImageSettings{}.MemoryEnvelope(), "non-positive values fall back to the defaults")
}

// TestSizeImageMemory: a defaulted decode budget is lowered to fit half the
// memory limit, never raised and never below the floor; an explicit budget is
// kept; an envelope that still does not fit is reported.
func TestSizeImageMemory(t *testing.T) {
	defaults := defaultEnvelopeSettings()
	fixed := defaults.MemoryEnvelope().InputBuffers + defaults.MemoryEnvelope().PreparedOutputs

	got, sizing := SizeImageMemory(defaults, 0)
	require.Equal(t, defaults, got, "no limit: unchanged")
	require.True(t, sizing.Fits)

	got, sizing = SizeImageMemory(defaults, 8<<30)
	require.Equal(t, defaults, got, "a roomy limit keeps the default")
	require.True(t, sizing.Fits)
	require.Zero(t, sizing.LoweredFrom)

	limit := int64(4 << 30)
	got, sizing = SizeImageMemory(defaults, limit)
	require.Equal(t, limit/2-fixed, got.DecodeMemoryBudgetBytes)
	require.Equal(t, DefaultImageDecodeMemoryBudgetBytes, sizing.LoweredFrom)
	require.True(t, sizing.Fits)
	require.Equal(t, limit/2, sizing.Envelope.Total())

	got, sizing = SizeImageMemory(defaults, 2<<30)
	require.Equal(t, minAutoDecodeBudgetBytes, got.DecodeMemoryBudgetBytes, "floored")
	require.False(t, sizing.Fits, "the create caps alone exceed half of 2 GiB")

	fewer := defaults
	fewer.MaxConcurrentCreates = 2
	got, sizing = SizeImageMemory(fewer, 2<<30)
	require.True(t, sizing.Fits, "lowering max_concurrent_requests makes it fit")
	require.Greater(t, got.DecodeMemoryBudgetBytes, minAutoDecodeBudgetBytes)

	explicit := defaults
	explicit.DecodeMemoryBudgetConfigured = true
	got, sizing = SizeImageMemory(explicit, 2<<30)
	require.Equal(t, DefaultImageDecodeMemoryBudgetBytes, got.DecodeMemoryBudgetBytes, "an explicit budget is kept")
	require.False(t, sizing.Fits)
	require.Zero(t, sizing.LoweredFrom)
}
