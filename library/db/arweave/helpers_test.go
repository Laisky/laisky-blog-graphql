package arweave

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newTestArdrive binds an uploader to a fake gateway with millisecond retry
// delays, so retry tests stay fast without changing retry semantics.
func newTestArdrive(t *testing.T, walletPath, gatewayURL string) *Ardrive {
	t.Helper()
	a := newArdrive(walletPath, gatewayURL)
	require.NotNil(t, a.signer)
	a.gateway.retryBaseDelay = time.Millisecond
	a.gateway.retryMaxDelay = 5 * time.Millisecond
	return a
}

// setRequestBounds tightens the per-attempt timeout and the attempt limit.
func setRequestBounds(a *Ardrive, timeout time.Duration, attempts int) {
	a.gateway.requestTimeout = timeout
	a.gateway.maxAttempts = attempts
}

// setCommitTimeout tightens the bound on finishing a committed upload.
func setCommitTimeout(a *Ardrive, timeout time.Duration) {
	a.commitTimeout = timeout
}
