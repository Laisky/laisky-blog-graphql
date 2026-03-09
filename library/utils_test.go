package library

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStripBearerPrefix(t *testing.T) {
	cases := map[string]struct {
		input    string
		expected string
	}{
		"empty":             {input: "", expected: ""},
		"whitespace":        {input: "   \t", expected: ""},
		"token only":        {input: "token123", expected: "token123"},
		"prefixed":          {input: "Bearer token123", expected: "token123"},
		"mixed case":        {input: "bEaReR token123", expected: "token123"},
		"extra spaces":      {input: "Bearer    token123   ", expected: "token123"},
		"multiple prefixes": {input: "Bearer Bearer token123", expected: "token123"},
		"with identity":     {input: "Bearer user:ai@token123", expected: "user:ai@token123"},
		"leading spaces":    {input: "   Bearer token123", expected: "token123"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			result := StripBearerPrefix(tc.input)
			require.Equal(t, tc.expected, result)
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		n        int
		expected string
	}{
		{"empty", "", 5, ""},
		{"short", "abc", 5, "abc"},
		{"exact", "abcde", 5, "abcde"},
		{"long", "abcdef", 5, "abcde"},
		{"utf8_short", "你好", 5, "你好"},
		{"utf8_exact", "你好世界", 4, "你好世界"},
		{"utf8_long", "你好世界！", 4, "你好世界"},
		{"zero", "abc", 0, "abc"},
		{"negative", "abc", -1, "abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, Truncate(tt.s, tt.n))
		})
	}
}
