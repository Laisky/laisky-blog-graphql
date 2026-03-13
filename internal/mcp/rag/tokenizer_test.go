package rag

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTokenize(t *testing.T) {
	tokens := Tokenize("Hello, HELLO world!! world")
	require.Len(t, tokens, 2, "expected 2 unique tokens")
	require.Equal(t, "hello", tokens[0], "unexpected token order")
}
