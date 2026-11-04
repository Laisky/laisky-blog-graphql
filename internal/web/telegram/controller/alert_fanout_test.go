package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
)

// alertControllerAttempt records only synthetic per-recipient calls in invocation order.
type alertControllerAttempt struct {
	uid int
	msg string
}

// alertControllerScenario supplies controlled authentication, subscribers and send outcomes.
type alertControllerScenario struct {
	recordingTelegram
	authCalls int
	loadCalls int
	authErr   error
	users     []*model.MonitorUsers
	attempts  []alertControllerAttempt
	send      func(context.Context, int, string) error
}

// ValidateTokenForAlertType records authentication before any subscriber or transport activity.
func (s *alertControllerScenario) ValidateTokenForAlertType(_ context.Context, _, name string) (*model.AlertTypes, error) {
	s.authCalls++
	if s.authErr != nil {
		return nil, s.authErr
	}
	return &model.AlertTypes{Name: name}, nil
}

// LoadUsersByAlertType returns the exact synthetic subscription snapshot.
func (s *alertControllerScenario) LoadUsersByAlertType(context.Context, *model.AlertTypes) ([]*model.MonitorUsers, error) {
	s.loadCalls++
	return s.users, nil
}

// SendAlertToUser records one explicit attempt and delegates only to the local fixture.
func (s *alertControllerScenario) SendAlertToUser(ctx context.Context, uid int, msg string) error {
	s.attempts = append(s.attempts, alertControllerAttempt{uid: uid, msg: msg})
	if s.send != nil {
		return s.send(ctx, uid, msg)
	}
	return nil
}

// TestAlertControllerAuthenticationPrecedesSend never sends or loads subscribers for invalid credentials.
func TestAlertControllerAuthenticationPrecedesSend(t *testing.T) {
	enableAlertThrottle(t)
	authErr := errors.New("synthetic authentication rejection")
	svc := &alertControllerScenario{
		authErr: authErr,
		users:   []*model.MonitorUsers{{UID: 11}},
	}
	alert, err := NewMutationResolver(svc).TelegramMonitorAlert(context.Background(), "synthetic_type", "synthetic-token", "chosen original body")
	require.ErrorIs(t, err, authErr)
	require.Nil(t, alert)
	require.Equal(t, 1, svc.authCalls)
	require.Zero(t, svc.loadCalls)
	require.Empty(t, svc.attempts)
}

// TestAlertControllerRejectsWholePayloadBeforeSideEffects validates full framed bytes before throttle or auth.
func TestAlertControllerRejectsWholePayloadBeforeSideEffects(t *testing.T) {
	const alertType = "synthetic_type"
	prefix := alertType + " >>>>>>>>>>>>>>>>>> \n"
	require.Equal(t, 2*1024*1024, service.MaxAlertPayloadBytes)
	for _, tc := range []struct {
		name string
		kind string
		msg  string
	}{
		{"complete framed size above limit", alertType, strings.Repeat("x", service.MaxAlertPayloadBytes-len(prefix)+1)},
		{"body alone at limit still exceeds framing limit", alertType, strings.Repeat("x", service.MaxAlertPayloadBytes)},
		{"invalid message UTF8", alertType, "chosen prefix" + string([]byte{255})},
		{"invalid alert type UTF8", string([]byte{255}), "chosen body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetThrottleConfig(t)
			require.Nil(t, telegramRatelimiter)
			svc := &alertControllerScenario{users: []*model.MonitorUsers{{UID: 11}}}
			_, err := NewMutationResolver(svc).TelegramMonitorAlert(context.Background(), tc.kind, "synthetic-token", tc.msg)
			require.Error(t, err)
			require.NotErrorIs(t, err, errTelegramThrottleUnavailable, "malformed payload must fail before throttle admission")
			require.Zero(t, svc.authCalls)
			require.Zero(t, svc.loadCalls)
			require.Empty(t, svc.attempts)
		})
	}
}

// TestAlertControllerAcceptsExactWholePayloadLimit preserves all bytes at the declared admission boundary.
func TestAlertControllerAcceptsExactWholePayloadLimit(t *testing.T) {
	enableAlertThrottle(t)
	const alertType = "synthetic_type"
	prefix := alertType + " >>>>>>>>>>>>>>>>>> \n"
	msg := strings.Repeat("x", service.MaxAlertPayloadBytes-len(prefix)-len("\n原文尾部")) + "\n原文尾部"
	svc := &alertControllerScenario{users: []*model.MonitorUsers{{UID: 11}}}
	_, err := NewMutationResolver(svc).TelegramMonitorAlert(context.Background(), alertType, "synthetic-token", msg)
	require.NoError(t, err)
	require.Len(t, svc.attempts, 1)
	require.Len(t, svc.attempts[0].msg, service.MaxAlertPayloadBytes)
	require.Equal(t, prefix+msg, svc.attempts[0].msg)
}

// TestAlertControllerDeduplicatesWithinOneRequest attempts each distinct subscriber exactly once.
func TestAlertControllerDeduplicatesWithinOneRequest(t *testing.T) {
	enableAlertThrottle(t)
	svc := &alertControllerScenario{users: []*model.MonitorUsers{{UID: 11}, {UID: 11}, {UID: 22}, {UID: 11}, {UID: 22}}}
	_, err := NewMutationResolver(svc).TelegramMonitorAlert(context.Background(), "synthetic_type", "synthetic-token", "<literal> _original_")
	require.NoError(t, err)
	require.Equal(t, []alertControllerAttempt{
		{uid: 11, msg: "synthetic_type >>>>>>>>>>>>>>>>>> \n<literal> _original_"},
		{uid: 22, msg: "synthetic_type >>>>>>>>>>>>>>>>>> \n<literal> _original_"},
	}, svc.attempts)
}

// TestAlertControllerInvalidSubscribersFailBeforeFanout exposes absent or invalid destinations without partial delivery.
func TestAlertControllerInvalidSubscribersFailBeforeFanout(t *testing.T) {
	for _, tc := range []struct {
		name  string
		users []*model.MonitorUsers
	}{
		{"nil subscribers", nil},
		{"empty subscribers", []*model.MonitorUsers{}},
		{"nil subscriber", []*model.MonitorUsers{nil}},
		{"zero UID", []*model.MonitorUsers{{UID: 0}}},
		{"invalid later subscriber", []*model.MonitorUsers{{UID: 11}, nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableAlertThrottle(t)
			svc := &alertControllerScenario{users: tc.users}
			_, err := NewMutationResolver(svc).TelegramMonitorAlert(context.Background(), "synthetic_type", "synthetic-token", "chosen body")
			require.Error(t, err)
			require.Equal(t, 1, svc.authCalls)
			require.Equal(t, 1, svc.loadCalls)
			require.Empty(t, svc.attempts)
		})
	}
}

// TestAlertControllerPartialFanoutReportsCounts preserves explicit rejection versus unknown acceptance.
func TestAlertControllerPartialFanoutReportsCounts(t *testing.T) {
	enableAlertThrottle(t)
	const privateTransport = "SYNTHETIC_TRANSPORT_PRIVATE"
	svc := &alertControllerScenario{
		users: []*model.MonitorUsers{{UID: 11}, {UID: 22}, {UID: 33}, {UID: 44}},
		send: func(_ context.Context, uid int, _ string) error {
			switch uid {
			case 22:
				return &service.AlertDeliveryError{RetryAfterSeconds: 7}
			case 33:
				return &service.AlertDeliveryError{Unconfirmed: true}
			case 44:
				return errors.New(privateTransport)
			default:
				return nil
			}
		},
	}
	alert, err := NewMutationResolver(svc).TelegramMonitorAlert(context.Background(), "synthetic_type", "synthetic-token", "SYNTHETIC_ORIGINAL_BODY")
	require.NotNil(t, alert)
	var fanout *AlertFanoutError
	require.ErrorAs(t, err, &fanout)
	require.Equal(t, 4, fanout.Total)
	require.Equal(t, 4, fanout.Attempted)
	require.Equal(t, 1, fanout.Accepted)
	require.Equal(t, 1, fanout.Rejected)
	require.Equal(t, 2, fanout.Unconfirmed)
	require.Zero(t, fanout.Unattempted)
	require.Equal(t, 7, fanout.RetryAfterSeconds)
	require.Len(t, svc.attempts, 4, "partial failure must not retry or silently skip another recipient")
	require.NotContains(t, err.Error(), privateTransport)
	require.NotContains(t, err.Error(), "SYNTHETIC_ORIGINAL_BODY")
	require.NotErrorIs(t, err, context.Canceled)
}

// TestAlertControllerCancellationStopsLaterRecipients reports accepted and unattempted destinations separately.
func TestAlertControllerCancellationStopsLaterRecipients(t *testing.T) {
	enableAlertThrottle(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := &alertControllerScenario{
		users: []*model.MonitorUsers{{UID: 11}, {UID: 22}, {UID: 33}},
		send: func(_ context.Context, _ int, _ string) error {
			cancel()
			return nil
		},
	}
	_, err := NewMutationResolver(svc).TelegramMonitorAlert(ctx, "synthetic_type", "synthetic-token", "chosen body")
	require.ErrorIs(t, err, context.Canceled)
	var fanout *AlertFanoutError
	require.ErrorAs(t, err, &fanout)
	require.Equal(t, 3, fanout.Total)
	require.Equal(t, 1, fanout.Attempted)
	require.Equal(t, 1, fanout.Accepted)
	require.Zero(t, fanout.Rejected)
	require.Zero(t, fanout.Unconfirmed)
	require.Equal(t, 2, fanout.Unattempted)
	require.Len(t, svc.attempts, 1)
}

// TestAlertControllerExplicitRetryMayDuplicateAcceptedWork makes no cross-request exactly-once promise.
func TestAlertControllerExplicitRetryMayDuplicateAcceptedWork(t *testing.T) {
	enableAlertThrottle(t)
	first := true
	svc := &alertControllerScenario{
		users: []*model.MonitorUsers{{UID: 11}},
		send: func(_ context.Context, _ int, _ string) error {
			if first {
				first = false
				return &service.AlertDeliveryError{Unconfirmed: true}
			}
			return nil
		},
	}
	resolver := NewMutationResolver(svc)
	_, err := resolver.TelegramMonitorAlert(context.Background(), "synthetic_type", "synthetic-token", "identical chosen body")
	require.Error(t, err)
	require.Len(t, svc.attempts, 1, "an ambiguous result must not trigger an automatic retry")
	_, err = resolver.TelegramMonitorAlert(context.Background(), "synthetic_type", "synthetic-token", "identical chosen body")
	require.NoError(t, err)
	require.Len(t, svc.attempts, 2, "a new explicit request may deliver the same content again")
	require.Equal(t, svc.attempts[0], svc.attempts[1])
}

// TestAlertControllerEmptyBodyPreservesHeader keeps the direct API's existing header-only alert.
func TestAlertControllerEmptyBodyPreservesHeader(t *testing.T) {
	enableAlertThrottle(t)
	svc := &alertControllerScenario{users: []*model.MonitorUsers{{UID: 11}}}
	alert, err := NewMutationResolver(svc).TelegramMonitorAlert(context.Background(), "synthetic_type", "synthetic-token", "")
	require.NoError(t, err)
	require.NotNil(t, alert)
	require.Equal(t, "synthetic_type", alert.Name)
	require.Equal(t, 1, svc.authCalls)
	require.Equal(t, 1, svc.loadCalls)
	require.Equal(t, []alertControllerAttempt{{uid: 11, msg: "synthetic_type >>>>>>>>>>>>>>>>>> \n"}}, svc.attempts)
}
