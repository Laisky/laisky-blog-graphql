package telegram

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"
)

// TestAlertControllerFidelityPreservesCompleteInput exposes both default and configured clipping.
func TestAlertControllerFidelityPreservesCompleteInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		maxLen int
		input  string
	}{
		{
			name: "over 3000 Unicode runes and literal markup",
			input: strings.Repeat("原文🙂 <b>literal HTML</b> _under_ *stars* `code` [link](https://example.test/path?q=chosen)\n", 80) +
				"COMPLETE_SYNTHETIC_TAIL",
		},
		{
			name:   "legacy max_len twenty must not clip",
			maxLen: 20,
			input:  "原文🙂 literal <b>markup</b> _under_ *stars* `code` [link](https://example.test/a?q=chosen)\nCOMPLETE_SYNTHETIC_TAIL",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableAlertThrottle(t)
			previous := gconfig.Shared.GetInt("settings.telegram.max_len")
			gconfig.Shared.Set("settings.telegram.max_len", tc.maxLen)
			t.Cleanup(func() { gconfig.Shared.Set("settings.telegram.max_len", previous) })
			if tc.maxLen == 0 {
				require.Greater(t, utf8.RuneCountInString(tc.input), 3000)
			} else {
				require.Greater(t, utf8.RuneCountInString(tc.input), tc.maxLen)
			}
			svc := new(recordingTelegram)
			const alertType = "synthetic_literal_fidelity"
			alert, err := NewMutationResolver(svc).TelegramMonitorAlert(context.Background(), alertType, "synthetic-token", tc.input)
			require.NoError(t, err)
			require.NotNil(t, alert)
			require.Equal(t, alertType, alert.Name)
			require.Empty(t, svc.markdown, "literal application content must not be interpreted as Markdown")
			require.Equal(t, []string{alertType + " >>>>>>>>>>>>>>>>>> \n" + tc.input}, svc.plain,
				"the service must receive the entire original message, not a clipped prefix or marker")
		})
	}
}
