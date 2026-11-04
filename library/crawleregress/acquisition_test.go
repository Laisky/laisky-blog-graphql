package crawleregress

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestVerifyAcquisitionPreservesDirectEvidence keeps receipt checks mandatory
// when a result has legacy metadata or explicitly reports direct acquisition.
func TestVerifyAcquisitionPreservesDirectEvidence(t *testing.T) {
	for _, acquisition := range []*Acquisition{nil, {Method: AcquisitionDirect}} {
		policy, receipt := validReceipt()
		require.NoError(t, VerifyAcquisition(t.Context(), receipt.TaskID, receipt.TargetURL, policy, false, acquisition, receipt))
		require.ErrorIs(t, VerifyAcquisition(t.Context(), receipt.TaskID, receipt.TargetURL, policy, true, acquisition, nil), ErrUnverified)
		receipt.Origins[0].PeerAddress = "127.0.0.1"
		require.ErrorIs(t, VerifyAcquisition(t.Context(), receipt.TaskID, receipt.TargetURL, policy, true, acquisition, receipt), ErrRejected)
	}
}

// TestVerifyProviderAcquisition binds provider acceptance to explicit permission,
// a valid public policy and truthful metadata without a fabricated direct receipt.
func TestVerifyProviderAcquisition(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*Policy, *Acquisition, **Receipt, *bool)
		wantErr bool
	}{
		{name: "configured provider"},
		{name: "no hidden provider list", change: func(_ *Policy, a *Acquisition, _ **Receipt, _ *bool) { a.Provider = "custom_provider-2" }},
		{name: "maximum identifier", change: func(_ *Policy, a *Acquisition, _ **Receipt, _ *bool) { a.Provider = strings.Repeat("a", 64) }},
		{name: "missing capability", change: func(_ *Policy, _ *Acquisition, _ **Receipt, allowed *bool) { *allowed = false }, wantErr: true},
		{name: "direct receipt claimed", change: func(_ *Policy, _ *Acquisition, receipt **Receipt, _ *bool) { _, *receipt = validReceipt() }, wantErr: true},
		{name: "private target pins", change: func(policy *Policy, _ *Acquisition, _ **Receipt, _ *bool) { policy.Addresses = []string{"127.0.0.1"} }, wantErr: true},
		{name: "wrong policy host", change: func(policy *Policy, _ *Acquisition, _ **Receipt, _ *bool) { policy.Host = "other.invalid" }, wantErr: true},
		{name: "invalid redirect policy", change: func(policy *Policy, _ *Acquisition, _ **Receipt, _ *bool) { policy.MaxRedirects = -1 }, wantErr: true},
		{name: "unknown method", change: func(_ *Policy, a *Acquisition, _ **Receipt, _ *bool) { a.Method = "other" }, wantErr: true},
		{name: "empty method", change: func(_ *Policy, a *Acquisition, _ **Receipt, _ *bool) { a.Method = "" }, wantErr: true},
		{name: "direct method with provider", change: func(_ *Policy, a *Acquisition, _ **Receipt, _ *bool) { a.Method = AcquisitionDirect }, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy, direct := validReceipt()
			acquisition := &Acquisition{Method: AcquisitionProvider, Provider: "jina"}
			var receipt *Receipt
			allowed := true
			if tc.change != nil {
				tc.change(&policy, acquisition, &receipt, &allowed)
			}
			err := VerifyAcquisition(t.Context(), direct.TaskID, direct.TargetURL, policy, allowed, acquisition, receipt)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrRejected)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestProviderAcquisitionRejectsUnsafeIdentity covers malformed provider names,
// credential-bearing targets and missing task identity without network traffic.
func TestProviderAcquisitionRejectsUnsafeIdentity(t *testing.T) {
	policy, direct := validReceipt()
	for _, name := range []string{"", " ", "https://provider.invalid/token", "jina\nforged", "naïve", strings.Repeat("a", 65)} {
		a := &Acquisition{Method: AcquisitionProvider, Provider: name}
		require.ErrorIs(t, VerifyAcquisition(t.Context(), direct.TaskID, direct.TargetURL, policy, true, a, nil), ErrRejected)
	}
	a := &Acquisition{Method: AcquisitionProvider, Provider: "defuddle"}
	require.ErrorIs(t, VerifyAcquisition(t.Context(), "", direct.TargetURL, policy, true, a, nil), ErrRejected)
	require.ErrorIs(t, VerifyAcquisition(t.Context(), direct.TaskID, "https://user:secret@1.1.1.1/", policy, true, a, nil), ErrRejected)
	require.ErrorIs(t, VerifyAcquisition(t.Context(), direct.TaskID, "http://127.0.0.1/", policy, true, a, nil), ErrRejected)
}

// TestVerifyAcquisitionCancellation rejects both acquisition modes after the
// caller context ends and preserves the original cancellation sentinel.
func TestVerifyAcquisitionCancellation(t *testing.T) {
	policy, receipt := validReceipt()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, acquisition := range []*Acquisition{nil, {Method: AcquisitionDirect}, {Method: AcquisitionProvider, Provider: "jina"}} {
		require.ErrorIs(t, VerifyAcquisition(ctx, receipt.TaskID, receipt.TargetURL, policy, true, acquisition, receipt), context.Canceled)
		require.ErrorIs(t, VerifyAcquisition(nil, receipt.TaskID, receipt.TargetURL, policy, true, acquisition, receipt), ErrRejected)
	}
}
