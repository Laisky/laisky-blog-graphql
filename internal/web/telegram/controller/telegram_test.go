package telegram

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/formatting"
)

func TestEscapeMsgUsesSharedTelegramFormatting(t *testing.T) {
	input := "alert-name=value|{payload}`"
	require.Equal(t, formatting.EscapeTelegramMarkdown(input), escapeMsg(input))
}
