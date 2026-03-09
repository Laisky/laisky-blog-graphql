package formatting

import "strings"

var telegramMarkdownReplacer = strings.NewReplacer(
	"`", "'",
	"_", "\\_",
	"*", "\\*",
	"[", "\\[",
	"]", "\\]",
	"(", "\\(",
	")", "\\)",
	"~", "\\~",
	">", "\\>",
	"#", "\\#",
	"+", "\\+",
	"-", "\\-",
	"=", "\\=",
	"|", "\\|",
	"{", "\\{",
	"}", "\\}",
)

// EscapeTelegramMarkdown escapes Telegram Markdown metacharacters in msg.
// It preserves the existing project behavior by converting backticks to
// apostrophes instead of emitting escaped backticks.
func EscapeTelegramMarkdown(msg string) string {
	return telegramMarkdownReplacer.Replace(msg)
}
