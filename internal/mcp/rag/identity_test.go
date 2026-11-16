package rag

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizeTaskID(t *testing.T) {
	require.Equal(t, "workspace", SanitizeTaskID(" workspace "))
	require.Equal(t, "a-b", SanitizeTaskID("--a b--"))
	require.Empty(t, SanitizeTaskID("  "))
	require.Empty(t, SanitizeTaskID("!!!"))
	require.Len(t, SanitizeTaskID(strings.Repeat("x", 100)), 64)
}
