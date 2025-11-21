package arweave

import (
	"context"
	"encoding/json"
	"testing"
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
	if err != nil {
		t.Errorf("Marshal returned an error: %v", err)
	}

	fileID, err := akord.Upload(ctx, data) // WithContentType("application/json"),

	if err != nil {
		t.Errorf("Upload returned an error: %v", err)
	}

	t.Logf("FileID: %s", fileID)
	t.Error()

	// Add assertions for the expected fileID value or any other validation you need.
}
