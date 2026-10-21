package crawleregress

import (
	"context"

	"github.com/Laisky/errors/v2"
)

const (
	// AcquisitionDirect identifies content retrieved through the pinned renderer.
	AcquisitionDirect = "direct"
	// AcquisitionProvider identifies content returned by a configured provider.
	AcquisitionProvider = "provider"
)

// Acquisition reports how the trusted worker obtained the returned content.
// Provider metadata is not direct target connection evidence or attestation.
type Acquisition struct {
	Method   string `json:"method"`
	Provider string `json:"provider,omitempty"`
}

// VerifyAcquisition checks the acquisition mode against the submitted capability
// and public policy. Direct and legacy results require the original receipt;
// provider results require explicit permission and must not claim that receipt.
// The caller must independently bind the result task ID, target and policy to
// its submitted snapshot, and reject a provider result with a direct URL chain.
func VerifyAcquisition(ctx context.Context, taskID, target string, policy Policy, allowProviderFallback bool, acquisition *Acquisition, receipt *Receipt) error {
	if ctx == nil {
		return errors.Wrap(ErrRejected, "context required")
	}
	if err := ctx.Err(); err != nil {
		return errors.WithStack(err)
	}
	if acquisition == nil {
		return VerifyReceipt(ctx, taskID, target, policy, receipt)
	}
	switch acquisition.Method {
	case AcquisitionDirect:
		if acquisition.Provider != "" {
			return errors.Wrap(ErrRejected, "direct acquisition cannot name a provider")
		}
		return VerifyReceipt(ctx, taskID, target, policy, receipt)
	case AcquisitionProvider:
		if !allowProviderFallback || taskID == "" || !ValidProviderName(acquisition.Provider) || receipt != nil {
			return errors.Wrap(ErrRejected, "invalid provider acquisition")
		}
		if _, err := ValidatePolicy(ctx, target, policy); err != nil {
			return errors.Wrap(ErrRejected, "invalid provider target policy")
		}
		return nil
	default:
		return errors.Wrap(ErrRejected, "unknown acquisition method")
	}
}

// ValidProviderName reports whether name is a bounded diagnostic identifier.
// It validates syntax only; configured provider selection belongs to the worker.
func ValidProviderName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}
