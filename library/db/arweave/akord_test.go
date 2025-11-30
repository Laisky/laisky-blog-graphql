package arweave

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAkrod_Upload(t *testing.T) {
	apis := []string{
		// "api-key-1",
		// "api-key-2",
	}
	if len(apis) == 0 {
		t.Skip("no api keys provided")
	}
	akord := NewAkrod(apis)

	ctx := context.Background()
	data, err := json.Marshal([]string{"hello", "world"})
	require.NoError(t, err, "Marshal returned an error")

	fileID, err := akord.Upload(ctx, data) // WithContentType("application/json"),
	require.NoError(t, err, "Upload returned an error")

	t.Logf("FileID: %s", fileID)
	require.NotEmpty(t, fileID, "FileID should not be empty")
}
