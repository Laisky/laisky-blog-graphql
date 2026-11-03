package telegram

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	gconfig "github.com/Laisky/go-config/v2"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
)

const (
	webhookAlertType = "synthetic_webhook_alert"
	webhookToken     = "synthetic-webhook-push-token"
	webhookHeader    = webhookAlertType + " >>>>>>>>>>>>>>>>>> \n"

	// openIncidentBody follows the published version 1.2 schema, including the
	// fields this receiver ignores.
	openIncidentBody = `{
  "version": "1.2",
  "incident": {
    "incident_id": "0.synthetic",
    "scoping_project_id": "synthetic-project",
    "scoping_project_number": 12345,
    "url": "https://console.cloud.google.com/monitoring/alerting/alerts/0.synthetic?project=synthetic-project",
    "started_at": 1762000000,
    "ended_at": null,
    "state": "open",
    "summary": "Uptime check on the gateway failed from 2 regions.",
    "apigee_url": "https://apigee.example.test",
    "observed_value": "1.000",
    "resource": {"type": "uptime_url", "labels": {"host": "gateway.example.test"}},
    "resource_type_display_name": "Uptime Check URL",
    "resource_id": "1234",
    "resource_display_name": "gateway.example.test",
    "resource_name": "projects/synthetic-project/uptimeCheckConfigs/gateway",
    "metric": {"type": "monitoring.googleapis.com/uptime_check/check_passed", "displayName": "Check passed", "labels": {}},
    "metadata": {"system_labels": {}, "user_labels": {}},
    "policy_name": "CVM Uptime Failure",
    "policy_user_labels": {"tier": "p0"},
    "documentation": {"content": "Runbook", "mime_type": "text/markdown", "subject": "Uptime", "links": []},
    "condition": {"name": "projects/synthetic-project/alertPolicies/1/conditions/2", "displayName": "Uptime check failing"},
    "condition_name": "Uptime check failing",
    "threshold_value": "1",
    "severity": "Critical"
  }
}`

	openIncidentMessage = "[PROD] OPEN CRITICAL: CVM Uptime Failure\n" +
		"condition: Uptime check failing\n" +
		"resource: gateway.example.test\n" +
		"Uptime check on the gateway failed from 2 regions.\n" +
		"started: 2025-11-01T12:26:40Z\n" +
		"https://console.cloud.google.com/monitoring/alerting/alerts/0.synthetic?project=synthetic-project"
)

// webhookTelegram authenticates one synthetic credential and can fail on demand.
// It is safe for use from the httptest server goroutine.
type webhookTelegram struct {
	recordingTelegram
	mu          sync.Mutex
	validateErr error
	sendErr     error
	sent        []string
}

// ValidateTokenForAlertType accepts only the synthetic credential.
func (w *webhookTelegram) ValidateTokenForAlertType(_ context.Context, token, alertType string) (*model.AlertTypes, error) {
	if w.validateErr != nil {
		return nil, w.validateErr
	}
	if token != webhookToken || alertType != webhookAlertType {
		return nil, errors.WithStack(service.ErrAlertUnauthorized)
	}
	return &model.AlertTypes{Name: alertType}, nil
}

// SendAlertToUser records one alert, or returns the configured failure.
func (w *webhookTelegram) SendAlertToUser(_ context.Context, _ int, msg string) error {
	if w.sendErr != nil {
		return w.sendErr
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sent = append(w.sent, msg)
	return nil
}

// messages returns a copy of the alerts recorded so far.
func (w *webhookTelegram) messages() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.sent...)
}

// webhookResponse is the part of a response that the webhook contract defines.
type webhookResponse struct {
	status int
	header http.Header
	body   string
}

// startWebhook serves svc over real loopback HTTP, so the client and server
// body and auth handling are the ones a real sender would meet.
func startWebhook(t *testing.T, svc service.Interface) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(NewCloudMonitoringWebhook(svc))
	t.Cleanup(server.Close)
	return server
}

// postWebhook sends one notification and returns the response. Credentials are
// skipped when both username and password are empty.
func postWebhook(t *testing.T, url, username, password, body string) webhookResponse {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	if username != "" || password != "" {
		request.SetBasicAuth(username, password)
	}
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return webhookResponse{status: response.StatusCode, header: response.Header, body: string(raw)}
}

// TestCloudMonitoringChallengeThenDelivery follows the webhook_basicauth exchange:
// the first request has no credentials and must be challenged, and the repeat
// with credentials must deliver the exact alert text.
func TestCloudMonitoringChallengeThenDelivery(t *testing.T) {
	enableAlertThrottle(t)
	svc := new(webhookTelegram)
	server := startWebhook(t, svc)

	first := postWebhook(t, server.URL+"?env=prod", "", "", openIncidentBody)
	require.Equal(t, http.StatusUnauthorized, first.status)
	require.Equal(t, cloudMonitoringChallenge, first.header.Get("WWW-Authenticate"))
	require.Empty(t, svc.messages(), "an unauthenticated request must not send")

	second := postWebhook(t, server.URL+"?env=prod", webhookAlertType, webhookToken, openIncidentBody)
	require.Equal(t, http.StatusNoContent, second.status)
	require.Empty(t, second.body)
	require.Equal(t, []string{webhookHeader + openIncidentMessage}, svc.messages())
}

// TestCloudMonitoringResolvedAndSparseIncidents pins the rendered text for closed,
// sparse and test notifications, including every accepted timestamp spelling.
func TestCloudMonitoringResolvedAndSparseIncidents(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		body  string
		want  string
	}{
		{
			name:  "closed incident reports its end",
			query: "?env=staging",
			body:  `{"version":"1.2","incident":{"state":"closed","severity":"Warning","policy_name":"Disk","started_at":"1762000000","ended_at":1762000060.5}}`,
			want:  "[STAGING] RESOLVED WARNING: Disk\nstarted: 2025-11-01T12:26:40Z\nended: 2025-11-01T12:27:40Z",
		},
		{
			name: "project labels the alert without env",
			body: `{"version":"1.2","incident":{"scoping_project_id":"synthetic-project","state":"open","severity":"No severity","policy_name":"Disk"}}`,
			want: "[SYNTHETIC-PROJECT] OPEN UNSPECIFIED: Disk",
		},
		{
			name: "console test notification with an empty incident",
			body: `{"version":"test","incident":{}}`,
			want: "[UNKNOWN] OPEN UNSPECIFIED: unnamed policy",
		},
		{
			name: "null and empty timestamps are absent",
			body: `{"incident":{"policy_name":"Disk","started_at":null,"ended_at":""}}`,
			want: "[UNKNOWN] OPEN UNSPECIFIED: Disk",
		},
		{
			name: "literal markup and unicode stay intact",
			body: `{"incident":{"policy_name":"a_b *c* <d> 原文🙂","summary":"[x](https://example.test/?a=1&b=2)"}}`,
			want: "[UNKNOWN] OPEN UNSPECIFIED: a_b *c* <d> 原文🙂\n[x](https://example.test/?a=1&b=2)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableAlertThrottle(t)
			svc := new(webhookTelegram)
			server := startWebhook(t, svc)
			response := postWebhook(t, server.URL+tc.query, webhookAlertType, webhookToken, tc.body)
			require.Equal(t, http.StatusNoContent, response.status)
			require.Equal(t, []string{webhookHeader + tc.want}, svc.messages())
		})
	}
}

// TestCloudMonitoringRefusesBadCredentials proves every refused credential gets
// the same challenge, sends nothing and leaves the alert type's budget intact.
func TestCloudMonitoringRefusesBadCredentials(t *testing.T) {
	resetThrottleConfig(t)
	for _, key := range throttleConfigKeys {
		gconfig.Shared.Set(key, 1)
	}
	NewTelegram(context.Background(), nil)
	svc := new(webhookTelegram)
	server := startWebhook(t, svc)

	for _, tc := range []struct {
		name, username, password string
	}{
		{name: "wrong token", username: webhookAlertType, password: "wrong"},
		{name: "unknown type", username: "unknown", password: webhookToken},
		{name: "empty token", username: webhookAlertType},
		{name: "empty type", password: webhookToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := postWebhook(t, server.URL, tc.username, tc.password, openIncidentBody)
			require.Equal(t, http.StatusUnauthorized, response.status)
			require.Equal(t, cloudMonitoringChallenge, response.header.Get("WWW-Authenticate"))
		})
	}
	require.Empty(t, svc.messages())

	response := postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody)
	require.Equal(t, http.StatusNoContent, response.status, "refused requests must not spend the send budget")
}

// TestCloudMonitoringOutageIsNotAnAuthFailure keeps a storage failure retryable.
// Reporting it as 401 would present an outage as rotated credentials.
func TestCloudMonitoringOutageIsNotAnAuthFailure(t *testing.T) {
	enableAlertThrottle(t)
	svc := &webhookTelegram{validateErr: errors.New("mongo unavailable")}
	server := startWebhook(t, svc)
	response := postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody)
	require.Equal(t, http.StatusServiceUnavailable, response.status)
	require.Empty(t, response.header.Get("WWW-Authenticate"))
	require.Empty(t, svc.messages())
}

// TestCloudMonitoringRefusesInvalidRequests covers input that no retry can fix.
func TestCloudMonitoringRefusesInvalidRequests(t *testing.T) {
	for _, tc := range []struct {
		name   string
		query  string
		body   string
		status int
	}{
		{name: "malformed json", body: `{"incident":`, status: http.StatusBadRequest},
		{name: "trailing data", body: `{"incident":{}} extra`, status: http.StatusBadRequest},
		{name: "not an object", body: `[]`, status: http.StatusBadRequest},
		{name: "no incident", body: `{"version":"1.2"}`, status: http.StatusBadRequest},
		{name: "null incident", body: `{"version":"1.2","incident":null}`, status: http.StatusBadRequest},
		{name: "invalid timestamp", body: `{"incident":{"started_at":"soon"}}`, status: http.StatusBadRequest},
		{name: "env with markup", query: "?env=%3Cb%3E", body: openIncidentBody, status: http.StatusBadRequest},
		{name: "env too long", query: "?env=" + strings.Repeat("a", 33), body: openIncidentBody, status: http.StatusBadRequest},
		{name: "body over the limit", body: strings.Repeat(" ", maxCloudMonitoringBodyBytes+1), status: http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableAlertThrottle(t)
			svc := new(webhookTelegram)
			server := startWebhook(t, svc)
			response := postWebhook(t, server.URL+tc.query, webhookAlertType, webhookToken, tc.body)
			require.Equal(t, tc.status, response.status)
			require.Empty(t, svc.messages())
		})
	}
}

// TestCloudMonitoringBodyAtTheLimitIsDelivered pins the limit as inclusive.
func TestCloudMonitoringBodyAtTheLimitIsDelivered(t *testing.T) {
	enableAlertThrottle(t)
	svc := new(webhookTelegram)
	server := startWebhook(t, svc)
	body := `{"incident":{"policy_name":"Disk"}}`
	body += strings.Repeat(" ", maxCloudMonitoringBodyBytes-len(body))
	response := postWebhook(t, server.URL, webhookAlertType, webhookToken, body)
	require.Equal(t, http.StatusNoContent, response.status)
	require.Len(t, svc.messages(), 1)
}

// TestCloudMonitoringDeliveryFailures keeps every undelivered alert visible to the
// sender, because a 2xx answer would end its only chance of a retry.
func TestCloudMonitoringDeliveryFailures(t *testing.T) {
	t.Run("provider rejection carries its retry hint", func(t *testing.T) {
		enableAlertThrottle(t)
		svc := &webhookTelegram{sendErr: &service.AlertDeliveryError{RetryAfterSeconds: 7}}
		server := startWebhook(t, svc)
		response := postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody)
		require.Equal(t, http.StatusServiceUnavailable, response.status)
		require.Equal(t, strconv.Itoa(7), response.header.Get("Retry-After"))
	})

	t.Run("unconfirmed delivery has no retry hint", func(t *testing.T) {
		enableAlertThrottle(t)
		svc := &webhookTelegram{sendErr: &service.AlertDeliveryError{Unconfirmed: true}}
		server := startWebhook(t, svc)
		response := postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody)
		require.Equal(t, http.StatusServiceUnavailable, response.status)
		require.Empty(t, response.header.Get("Retry-After"))
	})

	t.Run("missing throttle refuses to send unlimited alerts", func(t *testing.T) {
		resetThrottleConfig(t)
		svc := new(webhookTelegram)
		server := startWebhook(t, svc)
		response := postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody)
		require.Equal(t, http.StatusServiceUnavailable, response.status)
		require.Empty(t, svc.messages())
	})

	t.Run("exhausted budget answers 429", func(t *testing.T) {
		resetThrottleConfig(t)
		for _, key := range throttleConfigKeys {
			gconfig.Shared.Set(key, 1)
		}
		NewTelegram(context.Background(), nil)
		svc := new(webhookTelegram)
		server := startWebhook(t, svc)
		statuses := make(map[int]int)
		for range 5 {
			statuses[postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status]++
		}
		require.Positive(t, statuses[http.StatusTooManyRequests])
		require.Len(t, svc.messages(), statuses[http.StatusNoContent])
	})
}

// TestThrottledAlertKeepsItsGraphQLErrorText pins the text the GraphQL error
// classifier matches, so naming the sentinel cannot change client-visible behavior.
func TestThrottledAlertKeepsItsGraphQLErrorText(t *testing.T) {
	resetThrottleConfig(t)
	for _, key := range throttleConfigKeys {
		gconfig.Shared.Set(key, 1)
	}
	NewTelegram(context.Background(), nil)
	resolver := NewMutationResolver(new(webhookTelegram))
	var denied error
	for range 5 {
		if _, err := resolver.TelegramMonitorAlert(context.Background(), webhookAlertType, webhookToken, "msg"); err != nil {
			denied = err
		}
	}
	require.EqualError(t, denied, "deny by throttle")
}

// TestCloudMonitoringNeverEchoesSecretsOrContent checks responses and every log
// level, across a success and each refusal path.
func TestCloudMonitoringNeverEchoesSecretsOrContent(t *testing.T) {
	enableAlertThrottle(t)
	const summaryCanary = "SYNTHETIC-ALERT-CONTENT-CANARY"
	var output bytes.Buffer
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&output), zap.DebugLevel)
	logger, err := glog.NewWithName("cloud_monitoring_test", glog.LevelDebug,
		zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
	require.NoError(t, err)

	svc := new(webhookTelegram)
	handler := NewCloudMonitoringWebhook(svc)
	for _, tc := range []struct {
		name, password, body string
	}{
		{name: "delivered", password: webhookToken, body: `{"incident":{"summary":"` + summaryCanary + `"}}`},
		{name: "wrong token", password: "wrong-" + webhookToken, body: `{"incident":{"summary":"` + summaryCanary + `"}}`},
		{name: "malformed", password: webhookToken, body: `{"summary":"` + summaryCanary},
	} {
		request := httptest.NewRequest(http.MethodPost, "/alerts/cloud-monitoring", strings.NewReader(tc.body))
		request = request.WithContext(gmw.SetLogger(request.Context(), logger))
		request.SetBasicAuth(webhookAlertType, tc.password)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		require.Empty(t, recorder.Body.String(), tc.name)
	}
	require.Len(t, svc.messages(), 1)
	require.Contains(t, svc.messages()[0], summaryCanary)

	require.NoError(t, logger.Sync())
	logged := output.String()
	require.Contains(t, logged, "forwarded cloud monitoring incident", "the capture must see the handler's logs")
	require.NotContains(t, logged, webhookToken)
	require.NotContains(t, logged, summaryCanary)
}
