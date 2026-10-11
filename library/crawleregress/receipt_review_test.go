package crawleregress

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/library/toolpolicy"
)

// TestURLOnlyEvidenceDoesNotEstablishPinnedPeer reproduces the old verifier accepting a claimed URL with no connection evidence.
func TestURLOnlyEvidenceDoesNotEstablishPinnedPeer(t *testing.T) {
	p := toolpolicy.EgressPolicy{Host: "1.1.1.1", Addresses: []string{"1.1.1.1"}, MaxRedirects: 1}
	require.NoError(t, toolpolicy.VerifyEgressChain(context.Background(), p, []string{"https://1.1.1.1/document"}), "legacy diagnostic helper only validates claimed URLs")
	require.ErrorIs(t, VerifyReceipt(context.Background(), "task", "https://1.1.1.1/document", p, nil), ErrUnverified, "URL claims alone cannot establish pinned-peer evidence")
}
