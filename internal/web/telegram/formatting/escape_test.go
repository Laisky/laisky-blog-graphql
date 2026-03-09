package formatting

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEscapeTelegramMarkdown(t *testing.T) {
	tests := map[string]struct {
		input    string
		expected string
	}{
		"plain text": {
			input:    "plain text",
			expected: "plain text",
		},
		"all escaped characters": {
			input:    "`_*[]()~>#+-=|{}",
			expected: "'\\_\\*\\[\\]\\(\\)\\~\\>\\#\\+\\-\\=\\|\\{\\}",
		},
		"reported punctuation": {
			input:    "alpha-beta=gamma|{delta}",
			expected: "alpha\\-beta\\=gamma\\|\\{delta\\}",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.expected, EscapeTelegramMarkdown(tc.input))
		})
	}
}
