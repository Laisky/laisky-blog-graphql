package library

import "testing"

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
		result := StripBearerPrefix(tc.input)
		if result != tc.expected {
			t.Fatalf("%s: expected %q, got %q", name, tc.expected, result)
		}
	}
}
