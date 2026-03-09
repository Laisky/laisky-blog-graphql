package library

import (
	"strings"
	"testing"
	"unsafe"

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
	tests := map[string]struct {
		input    string
		n        int
		expected string
	}{
		"ascii truncate":           {input: "abcdef", n: 3, expected: "abc"},
		"ascii at boundary":        {input: "abc", n: 3, expected: "abc"},
		"ascii over boundary":      {input: "abc", n: 5, expected: "abc"},
		"utf8 truncate":            {input: "你好世界", n: 2, expected: "你好"},
		"utf8 at boundary":         {input: "你好", n: 2, expected: "你好"},
		"mixed ascii and utf8":     {input: "a你好b", n: 3, expected: "a你好"},
		"zero means unchanged":     {input: "abc", n: 0, expected: "abc"},
		"negative means unchanged": {input: "abc", n: -1, expected: "abc"},
		"empty string stays empty": {input: "", n: 4, expected: ""},
		"truncate to one rune":     {input: "éclair", n: 1, expected: "é"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.expected, Truncate(tc.input, tc.n))
		})
	}
}

func TestTruncate_MemoryIsolation(t *testing.T) {
	input := strings.Repeat("你好", 8)
	truncated := Truncate(input, 3)

	require.Equal(t, "你好你", truncated)
	// Truncate clones truncated strings so persisted user input does not keep
	// the original large backing string alive; comparing StringData pointers
	// verifies the cloned result has distinct storage.
	require.NotEqual(t,
		uintptr(unsafe.Pointer(unsafe.StringData(input))),
		uintptr(unsafe.Pointer(unsafe.StringData(truncated))),
	)
}
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
