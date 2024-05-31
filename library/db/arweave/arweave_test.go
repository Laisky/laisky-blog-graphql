package arweave

import (
	"context"
	"testing"
)

func TestAkrod_Upload(t *testing.T) {
	apis := []string{"api-key-1", "api-key-2"}
	akord := NewAkrod(apis)

	ctx := context.Background()
	data := []byte("test data")

	fileID, err := akord.Upload(ctx, data)
	if err != nil {
		t.Errorf("Upload returned an error: %v", err)
	}

	t.Logf("FileID: %s", fileID)

	// Add assertions for the expected fileID value or any other validation you need.
}
