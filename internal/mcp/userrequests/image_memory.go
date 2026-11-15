package userrequests

import (
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/imageproc"
)

// multipartFormMemoryBytes is what one multipart.Reader.ReadForm call keeps in
// memory: up to maxMultipartMemory of file parts plus up to 10 MiB of other
// fields (mime/multipart's documented allowance); the rest spills to disk.
const multipartFormMemoryBytes int64 = maxMultipartMemory + 10<<20

const (
	// imageMemoryShareDivisor sets the part of the process memory limit the
	// image create path may use: half, leaving the rest to the other
	// subsystems and the collector. Measured under eight worst-case creates,
	// the peak RSS above idle was 1.01x the envelope with a Go soft memory
	// limit and 2.0-2.1x without one (the collector lets the heap reach about
	// twice the live set at GOGC=100), so half also keeps a process whose
	// soft limit was turned off below its limit.
	imageMemoryShareDivisor = 2
	// minAutoDecodeBudgetBytes floors a decode budget derived from the
	// memory limit; below it most photos would be refused.
	minAutoDecodeBudgetBytes int64 = 256 << 20
)

// ImageMemoryEnvelope is the worst-case memory the image create path of one
// process holds at once (proposal §3.9, invariant I1). The decode budget
// bounds the decoders' working sets; the other terms are static bounds the
// create caps imply.
type ImageMemoryEnvelope struct {
	// DecodeBudget is decode_memory_budget_bytes.
	DecodeBudget int64
	// InputBuffers is max_concurrent_requests x (the multipart form memory
	// plus one raw attachment, read one at a time).
	InputBuffers int64
	// PreparedOutputs is max_concurrent_requests x max_per_request x the
	// largest normalized PNG: every prepared image is held until its upload
	// ends.
	PreparedOutputs int64
}

// Total is the sum of the terms.
func (e ImageMemoryEnvelope) Total() int64 {
	return e.DecodeBudget + e.InputBuffers + e.PreparedOutputs
}

// MemoryEnvelope returns the envelope these settings allow, with the same
// fallbacks the image manager applies to non-positive values.
func (s ImageSettings) MemoryEnvelope() ImageMemoryEnvelope {
	creates := int64(s.MaxConcurrentCreates)
	if creates <= 0 {
		creates = DefaultImageMaxConcurrentCreates
	}
	perRequest := int64(s.MaxPerRequest)
	if perRequest <= 0 {
		perRequest = DefaultImageMaxPerRequest
	}
	perImage := s.PerImageMaxBytes
	if perImage <= 0 {
		perImage = DefaultImagePerImageMaxBytes
	}
	budget := s.DecodeMemoryBudgetBytes
	if budget <= 0 {
		budget = DefaultImageDecodeMemoryBudgetBytes
	}
	return ImageMemoryEnvelope{
		DecodeBudget:    budget,
		InputBuffers:    creates * (multipartFormMemoryBytes + perImage),
		PreparedOutputs: creates * perRequest * imageproc.MaxNormalizedPNGBytes,
	}
}

// ImageMemorySizing reports how SizeImageMemory treated the settings.
type ImageMemorySizing struct {
	// Limit is the process memory limit considered (0 when none is known).
	Limit int64
	// Envelope is the envelope of the returned settings.
	Envelope ImageMemoryEnvelope
	// LoweredFrom is the decode budget before it was lowered to fit the
	// limit, or 0 when it was kept.
	LoweredFrom int64
	// Fits reports whether the envelope is within the image share of the
	// limit (always true without a limit).
	Fits bool
}

// SizeImageMemory fits the image create path into limit, the process memory
// limit in bytes (0 when none applies), and returns the settings to use. When
// the decode budget was not configured explicitly it is lowered, never
// raised, so the envelope stays within limit/imageMemoryShareDivisor, but not
// below minAutoDecodeBudgetBytes; an explicitly configured budget is kept.
// Fits is false when the envelope still exceeds the share, which the caller
// reports so the operator lowers max_concurrent_requests or the budget.
func SizeImageMemory(settings ImageSettings, limit int64) (ImageSettings, ImageMemorySizing) {
	envelope := settings.MemoryEnvelope()
	sizing := ImageMemorySizing{Limit: limit, Envelope: envelope, Fits: true}
	if limit <= 0 {
		return settings, sizing
	}
	share := limit / imageMemoryShareDivisor
	if envelope.Total() <= share {
		return settings, sizing
	}
	if !settings.DecodeMemoryBudgetConfigured {
		fixed := envelope.InputBuffers + envelope.PreparedOutputs
		budget := max(share-fixed, minAutoDecodeBudgetBytes)
		if budget < envelope.DecodeBudget {
			sizing.LoweredFrom = envelope.DecodeBudget
			settings.DecodeMemoryBudgetBytes = budget
			envelope = settings.MemoryEnvelope()
			sizing.Envelope = envelope
		}
	}
	sizing.Fits = envelope.Total() <= share
	return settings, sizing
}
