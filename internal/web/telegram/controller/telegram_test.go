package telegram

import (
	"context"
	"strings"
	"testing"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dto"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
)

// recordingTelegram is a service.Interface that records outgoing messages by
// the send method used, and resolves every alert token to one subscriber.
type recordingTelegram struct {
	markdown []string
	plain    []string
}

var _ service.Interface = new(recordingTelegram)

// PleaseRetry is unused by the alert path.
func (r *recordingTelegram) PleaseRetry(context.Context, *tb.User, string) {}

// SendMsgToUser records a Markdown-mode send.
func (r *recordingTelegram) SendMsgToUser(_ int, msg string) error {
	r.markdown = append(r.markdown, msg)
	return nil
}

// SendPlainTextToUser records a plain-text send.
func (r *recordingTelegram) SendPlainTextToUser(_ int, msg string) error {
	r.plain = append(r.plain, msg)
	return nil
}

// LoadAlertTypesByUser is unused by the alert path.
func (r *recordingTelegram) LoadAlertTypesByUser(context.Context, *model.MonitorUsers) ([]*model.AlertTypes, error) {
	return nil, nil
}

// LoadAlertTypes is unused by the alert path.
func (r *recordingTelegram) LoadAlertTypes(context.Context, *dto.QueryCfg) ([]*model.AlertTypes, error) {
	return nil, nil
}

// LoadUsers is unused by the alert path.
func (r *recordingTelegram) LoadUsers(context.Context, *dto.QueryCfg) ([]*model.MonitorUsers, error) {
	return nil, nil
}

// LoadUsersByAlertType returns one subscriber.
func (r *recordingTelegram) LoadUsersByAlertType(context.Context, *model.AlertTypes) ([]*model.MonitorUsers, error) {
	return []*model.MonitorUsers{{UID: 42}}, nil
}

// ValidateTokenForAlertType accepts every token for the requested type.
func (r *recordingTelegram) ValidateTokenForAlertType(_ context.Context, _, alertType string) (*model.AlertTypes, error) {
	return &model.AlertTypes{Name: alertType}, nil
}

// SetAskUserService is unused by the alert path.
func (r *recordingTelegram) SetAskUserService(*askuser.Service) {}

// enableAlertThrottle installs a permissive alert rate limiter for one test.
func enableAlertThrottle(t *testing.T) {
	t.Helper()
	resetThrottleConfig(t)
	for _, key := range throttleConfigKeys {
		gconfig.Shared.Set(key, 100)
	}
	NewTelegram(context.Background(), nil)
	require.NotNil(t, telegramRatelimiter)
}

// TestAlertIsDeliveredAsPlainText proves an alert reaches Telegram exactly as
// the sender wrote it. The gateway used to escape alerts for MarkdownV2 while
// sending them in legacy Markdown mode, so subscribers saw "[PROD\]" and
// "end\-to\-end", and the unescaped header lost its underscores to italics
// ("pieverseteewallet").
func TestAlertIsDeliveredAsPlainText(t *testing.T) {
	enableAlertThrottle(t)
	svc := new(recordingTelegram)
	resolver := NewMutationResolver(svc)

	msg := "[PROD] OPEN WARNING: Alert relay end-to-end test\ncondition: a_b *c* `d` (e).\nhttps://example.com/x?y=1"
	alert, err := resolver.TelegramMonitorAlert(context.Background(), "pieverse_tee_wallet", "token", msg)
	require.NoError(t, err)
	require.NotNil(t, alert)

	require.Empty(t, svc.markdown, "alerts must not be sent in Markdown mode")
	require.Equal(t, []string{"pieverse_tee_wallet >>>>>>>>>>>>>>>>>> \n" + msg}, svc.plain)
}

// TestLongAlertIsTruncatedWithoutEscaping proves an oversized alert is cut to
// the configured length and marked, still as plain text.
func TestLongAlertIsTruncatedWithoutEscaping(t *testing.T) {
	enableAlertThrottle(t)
	gconfig.Shared.Set("settings.telegram.max_len", 20)
	t.Cleanup(func() { gconfig.Shared.Set("settings.telegram.max_len", 0) })
	svc := new(recordingTelegram)

	_, err := NewMutationResolver(svc).TelegramMonitorAlert(context.Background(), "t", "token", strings.Repeat("a-b.", 20))
	require.NoError(t, err)
	require.Len(t, svc.plain, 1)
	body := strings.TrimPrefix(svc.plain[0], "t >>>>>>>>>>>>>>>>>> \n")
	require.True(t, strings.HasSuffix(body, "..."), body)
	require.NotContains(t, body, `\`)
	require.LessOrEqual(t, len(strings.TrimSuffix(body, "...")), 20)
}
