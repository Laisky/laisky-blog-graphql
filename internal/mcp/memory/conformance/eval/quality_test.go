package eval

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

type failingScoreWriter struct {
	err   error
	short bool
}

func (w failingScoreWriter) Write(p []byte) (int, error) {
	if w.short {
		return len(p) / 2, nil
	}
	return 0, w.err
}

// TestScorecardPropagatesWriterFailures rejects silent truncation of acceptance evidence.
func TestScorecardPropagatesWriterFailures(t *testing.T) {
	sentinel := errors.New("synthetic disk full")
	card := Scorecard{PluginName: "test", RunID: "run"}
	require.ErrorIs(t, card.WriteMarkdown(failingScoreWriter{err: sentinel}), sentinel)
	require.ErrorIs(t, card.WriteMarkdown(failingScoreWriter{short: true}), io.ErrShortWrite)
	var first, second bytes.Buffer
	require.NoError(t, card.WriteMarkdown(&first))
	require.NoError(t, card.WriteMarkdown(&second))
	require.Equal(t, first.String(), second.String())
	require.Contains(t, first.String(), "test")
}
