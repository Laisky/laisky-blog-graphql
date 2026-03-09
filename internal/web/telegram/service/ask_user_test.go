package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/formatting"
)

func TestEscapeMsgUsesSharedTelegramFormatting(t *testing.T) {
	input := "question-beta=gamma|{delta}`"
	require.Equal(t, formatting.EscapeTelegramMarkdown(input), escapeMsg(input))
}

func TestBuildAskUserIntroPrompt(t *testing.T) {
	t.Run("without payload", func(t *testing.T) {
		prompt := buildAskUserIntroPrompt(false)
		require.Contains(t, prompt, "ask\\_user")
		require.NotContains(t, prompt, "ask_user")
		require.True(t, strings.Contains(prompt, "OneAPI API key"))
		require.Contains(t, prompt, "wiki.laisky.com")
	})

	t.Run("with payload", func(t *testing.T) {
		prompt := buildAskUserIntroPrompt(true)
		require.Contains(t, prompt, "ask\\_user")
		require.Contains(t, prompt, "send the key as a normal message")
	})
}
