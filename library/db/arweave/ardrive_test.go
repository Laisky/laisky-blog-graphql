package arweave

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewArdrive_invalidWallet(t *testing.T) {
	a := NewArdrive("/nonexistent/wallet.json", "folder-id")
	require.NotNil(t, a)
	require.Nil(t, a.wallet, "wallet should be nil for invalid path")
}
