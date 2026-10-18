package crawlercapture

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestJournalWriteDoesNotMutateRenderer allows the same immutable record to be reused concurrently.
func TestJournalWriteDoesNotMutateRenderer(t *testing.T) {
	journal := newSyntheticJournal(t)
	record := syntheticJournalRecord(CapturedString("nonempty original converter input"))
	record.Renderer = &Renderer{RequestID: "synthetic-exchange", Kind: "document", Method: "GET"}
	before := *record.Renderer
	const count = 8
	results := make(chan error, count)
	var writes sync.WaitGroup
	for range count {
		writes.Add(1)
		go func() { defer writes.Done(); _, err := journal.Write(record); results <- err }()
	}
	writes.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	require.Equal(t, before, *record.Renderer, "normalizing omitted capture fields must not mutate the caller pointer")
	require.Empty(t, record.Renderer.URL.State)
	require.Empty(t, record.Renderer.DecodedBody.State)
	require.Equal(t, "nonempty original converter input", string(record.Converter.Input.Bytes()))
}

// TestBlobClonePreservesSources snapshots public decoder storage without copying immutable private sources.
func TestBlobClonePreservesSources(t *testing.T) {
	for _, value := range []Blob{CapturedString("exact immutable string"), Captured([]byte{255, 0, 7}), Captured(nil), NotReached()} {
		clone := value.Clone()
		require.NoError(t, clone.Validate())
		require.Equal(t, value.State, clone.State)
		require.Equal(t, value.Bytes(), clone.Bytes())
		copied := clone.Bytes()
		if len(copied) > 0 {
			copied[0] ^= 255
		}
		require.Equal(t, value.Bytes(), clone.Bytes())
	}
	original := Captured([]byte("decoded mutable bytes"))
	original.Data = original.Bytes()
	original.owned = nil
	clone := original.Clone()
	original.Data[0] = 'X'
	require.Equal(t, "decoded mutable bytes", string(clone.Bytes()))
	require.NoError(t, clone.Validate())
}
