package telegram

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
)

// cancelAtEOF reads r and cancels its request once the body has been read, as a
// sender that disconnects right after uploading would.
type cancelAtEOF struct {
	r      io.Reader
	cancel context.CancelFunc
}

// Read reads from r and cancels the request at the end of the body.
func (c *cancelAtEOF) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if errors.Is(err, io.EOF) {
		c.cancel()
	}
	return n, err
}

// TestCloudMonitoringRequestEndedBeforeAdmissionSpendsNoSend reproduces a
// review finding: the webhook checked its context only after the credential
// check, then read the body and spent the shared and the alert type's send
// without looking again. A sender that went away while its body was read still
// spent both sends on a delivery that could no longer happen, which could leave
// its retry throttled. The request now ends without spending either.
func TestCloudMonitoringRequestEndedBeforeAdmissionSpendsNoSend(t *testing.T) {
	// One shared and one per-type send that do not refill while the test runs.
	configureAlertThrottle(t, 1, 1, 1, 1)
	svc := &webhookTelegram{send: func(ctx context.Context) error { return errors.WithStack(ctx.Err()) }}
	handler := NewCloudMonitoringWebhook(svc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ended := httptest.NewRequest(http.MethodPost, "/webhook/gcp/alerts/cloud-monitoring",
		&cancelAtEOF{r: strings.NewReader(openIncidentBody), cancel: cancel}).WithContext(ctx)
	ended.SetBasicAuth(webhookAlertType, webhookToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, ended)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Zero(t, telegramRatelimiter.Stats().Admitted, "a request that ended spends no delivery capacity")
	require.Empty(t, svc.messages())

	retry := httptest.NewRequest(http.MethodPost, "/webhook/gcp/alerts/cloud-monitoring", strings.NewReader(openIncidentBody))
	retry.SetBasicAuth(webhookAlertType, webhookToken)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, retry)
	require.Equal(t, http.StatusNoContent, recorder.Code, "the sender's retry still has its send")
	require.Len(t, svc.messages(), 1)
}

// lookupEndsRequestTelegram ends the request once while its credential lookup
// succeeds, and fails sends whose request has ended, as the Telegram client does.
type lookupEndsRequestTelegram struct {
	*tokenGatedTelegram
	cancel atomic.Pointer[context.CancelFunc]
}

// ValidateTokenForAlertType checks the credential, then ends the request once.
func (s *lookupEndsRequestTelegram) ValidateTokenForAlertType(ctx context.Context, token, alertType string) (*model.AlertTypes, error) {
	alert, err := s.tokenGatedTelegram.ValidateTokenForAlertType(ctx, token, alertType)
	if cancel := s.cancel.Swap(nil); cancel != nil {
		(*cancel)()
	}
	return alert, err
}

// SendAlertToUser fails once the request has ended.
func (s *lookupEndsRequestTelegram) SendAlertToUser(ctx context.Context, uid int, msg string) error {
	if err := ctx.Err(); err != nil {
		return errors.WithStack(err)
	}
	return s.tokenGatedTelegram.SendAlertToUser(ctx, uid, msg)
}

// TestAlertRequestEndedDuringLookupKeepsNoTypeSend applies the same finding to
// the GraphQL mutation: a request that ended while its credential lookup ran
// kept its reserved per-type send, though the delivery could no longer happen,
// so the alert type's next genuine alert was throttled. The shared send stays
// spent, because it paid for the lookup; the per-type send is returned.
func TestAlertRequestEndedDuringLookupKeepsNoTypeSend(t *testing.T) {
	// Two shared sends and one per-type send that do not refill while the test runs.
	configureAlertThrottle(t, 2, 2, 1, 1)
	svc := &lookupEndsRequestTelegram{tokenGatedTelegram: &tokenGatedTelegram{tokens: map[string]string{"known_alert": "known-token"}}}
	resolver := NewMutationResolver(svc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.cancel.Store(&cancel)
	_, err := resolver.TelegramMonitorAlert(ctx, "known_alert", "known-token", "ended")
	require.ErrorIs(t, err, context.Canceled)
	stats := telegramRatelimiter.Stats()
	require.Zero(t, stats.Authorized, "a request that ended keeps no send")
	require.EqualValues(t, 1, stats.Canceled)

	_, err = resolver.TelegramMonitorAlert(t.Context(), "known_alert", "known-token", "genuine")
	require.NoError(t, err, "the alert type's next alert still has its send")
}
