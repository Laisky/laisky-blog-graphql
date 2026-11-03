package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	gconfig "github.com/Laisky/go-config/v2"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

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
	send        func(context.Context) error
	sent        []string
	validations int
}

// ValidateTokenForAlertType accepts only the synthetic credential.
func (w *webhookTelegram) ValidateTokenForAlertType(_ context.Context, token, alertType string) (*model.AlertTypes, error) {
	w.mu.Lock()
	w.validations++
	w.mu.Unlock()
	if w.validateErr != nil {
		return nil, w.validateErr
	}
	if token != webhookToken || alertType != webhookAlertType {
		return nil, errors.WithStack(service.ErrAlertUnauthorized)
	}
	return &model.AlertTypes{Name: alertType}, nil
}

// SendAlertToUser records one alert, or returns the configured failure.
func (w *webhookTelegram) SendAlertToUser(ctx context.Context, _ int, msg string) error {
	if w.sendErr != nil {
		return w.sendErr
	}
	if w.send != nil {
		if err := w.send(ctx); err != nil {
			return err
		}
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

// credentialChecks returns how many times a credential reached the service.
func (w *webhookTelegram) credentialChecks() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.validations
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
// sparse, test and unknown-state notifications, including every accepted
// timestamp spelling. No state other than a documented one reads as OPEN.
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
			name: "upper-case states are the documented ones",
			body: `{"version":"1.2","incident":{"state":"CLOSED","policy_name":"Disk"}}`,
			want: "[UNKNOWN] RESOLVED UNSPECIFIED: Disk",
		},
		{
			// Shape of the console's Test Connection payload as captured in
			// docs/ref/cloud_monitoring_webhook_20261103.md: populated and "OPEN".
			name: "console test connection",
			body: `{"version":"test","incident":{"incident_id":"12345","state":"OPEN","started_at":0,"ended_at":0,` +
				`"policy_name":"projects/12345/alertPolicies/12345","summary":"Test Incident","url":"http://www.example.com",` +
				`"documentation":{"content":"TEST ALERT","subject":"ALERT - No severity","mime_type":"text/markdown"}}}`,
			want: "[UNKNOWN] TEST UNSPECIFIED: projects/12345/alertPolicies/12345\nTest Incident\nhttp://www.example.com",
		},
		{
			name: "empty test incident",
			body: `{"version":"TEST","incident":{}}`,
			want: "[UNKNOWN] TEST UNSPECIFIED: unnamed policy",
		},
		{
			name: "undocumented state",
			body: `{"version":"1.2","incident":{"state":"acknowledged","policy_name":"Disk"}}`,
			want: "[UNKNOWN] UNKNOWN-STATE UNSPECIFIED: Disk",
		},
		{
			name: "missing state",
			body: `{"incident":{"policy_name":"Disk"}}`,
			want: "[UNKNOWN] UNKNOWN-STATE UNSPECIFIED: Disk",
		},
		{
			name: "null, empty and zero timestamps are absent",
			body: `{"incident":{"state":"open","policy_name":"Disk","started_at":0,"ended_at":""}}`,
			want: "[UNKNOWN] OPEN UNSPECIFIED: Disk",
		},
		{
			name: "null timestamp is absent",
			body: `{"incident":{"state":"open","policy_name":"Disk","started_at":null,"ended_at":null}}`,
			want: "[UNKNOWN] OPEN UNSPECIFIED: Disk",
		},
		{
			name: "last RFC 3339 second",
			body: `{"incident":{"state":"closed","policy_name":"Disk","ended_at":253402300799}}`,
			want: "[UNKNOWN] RESOLVED UNSPECIFIED: Disk\nended: 9999-12-31T23:59:59Z",
		},
		{
			name: "literal markup and unicode stay intact",
			body: `{"incident":{"state":"open","policy_name":"a_b *c* <d> 原文🙂","summary":"[x](https://example.test/?a=1&b=2)"}}`,
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
		request := httptest.NewRequest(http.MethodPost, "/webhook/gcp/alerts/cloud-monitoring", strings.NewReader(tc.body))
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

// TestCloudMonitoringChecksCredentialsOnce keeps one notification to one
// credential lookup, so a token rotated mid-request cannot turn into a 503.
func TestCloudMonitoringChecksCredentialsOnce(t *testing.T) {
	enableAlertThrottle(t)
	svc := new(webhookTelegram)
	server := startWebhook(t, svc)
	response := postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody)
	require.Equal(t, http.StatusNoContent, response.status)
	require.Equal(t, 1, svc.credentialChecks())
}

// TestCloudMonitoringBoundsRefusedCredentialLookups proves a caller without a
// valid push token cannot make the gateway query storage at its own pace, and
// that the budget never charges the credential-free challenge or an accepted
// credential.
func TestCloudMonitoringBoundsRefusedCredentialLookups(t *testing.T) {
	t.Run("refusals are bounded and answered with a retry hint", func(t *testing.T) {
		enableAlertThrottle(t)
		svc := new(webhookTelegram)
		server := startWebhook(t, svc)
		started := time.Now()
		statuses := make(map[int]int)
		var retryAfter string
		for range 4 * cloudMonitoringRefusalBurst {
			response := postWebhook(t, server.URL, webhookAlertType, "wrong", openIncidentBody)
			statuses[response.status]++
			if response.status == http.StatusTooManyRequests {
				retryAfter = response.header.Get("Retry-After")
				require.Empty(t, response.header.Get("WWW-Authenticate"))
			}
		}
		refilled := int(math.Ceil(time.Since(started).Seconds() * cloudMonitoringRefusalsPerSecond))
		require.LessOrEqual(t, svc.credentialChecks(), cloudMonitoringRefusalBurst+refilled)
		require.Equal(t, svc.credentialChecks(), statuses[http.StatusUnauthorized])
		require.Positive(t, statuses[http.StatusTooManyRequests])
		seconds, err := strconv.Atoi(retryAfter)
		require.NoError(t, err)
		require.Positive(t, seconds)
	})

	t.Run("accepted credentials and challenges are free", func(t *testing.T) {
		enableAlertThrottle(t)
		svc := new(webhookTelegram)
		server := startWebhook(t, svc)
		for range 2 * cloudMonitoringRefusalBurst {
			require.Equal(t, http.StatusUnauthorized, postWebhook(t, server.URL, "", "", openIncidentBody).status)
			require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status)
		}
		require.Equal(t, http.StatusUnauthorized, postWebhook(t, server.URL, webhookAlertType, "wrong", openIncidentBody).status)
	})

	t.Run("the budget recovers once guessing stops", func(t *testing.T) {
		enableAlertThrottle(t)
		svc := new(webhookTelegram)
		handler := NewCloudMonitoringWebhook(svc)
		handler.refusals = rate.NewLimiter(rate.Every(20*time.Millisecond), 1)
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		require.Equal(t, http.StatusUnauthorized, postWebhook(t, server.URL, webhookAlertType, "wrong", openIncidentBody).status)
		require.Equal(t, http.StatusTooManyRequests, postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status,
			"while the budget is spent even a valid credential waits; this is the documented trade-off")
		require.Eventually(t, func() bool {
			return postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status == http.StatusNoContent
		}, 5*time.Second, 25*time.Millisecond)
	})

	t.Run("storage failures are charged too", func(t *testing.T) {
		enableAlertThrottle(t)
		svc := &webhookTelegram{validateErr: errors.New("mongo unavailable")}
		server := startWebhook(t, svc)
		for range 2 * cloudMonitoringRefusalBurst {
			postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody)
		}
		require.Less(t, svc.credentialChecks(), 2*cloudMonitoringRefusalBurst)
	})
}

// TestCloudMonitoringRefusesUndefinedTimestamps pins the accepted spellings:
// anything else is a schema violation, not a guess.
func TestCloudMonitoringRefusesUndefinedTimestamps(t *testing.T) {
	for _, value := range []string{`1e300`, `1.7e9`, `"NaN"`, `"Inf"`, `-5`, `"-5"`, `99999999999999`, `253402300800`,
		`"0x10"`, `"1_0"`, `" 1"`, `"soon"`, `true`, `{}`, `[]`} {
		t.Run(value, func(t *testing.T) {
			enableAlertThrottle(t)
			svc := new(webhookTelegram)
			server := startWebhook(t, svc)
			response := postWebhook(t, server.URL, webhookAlertType, webhookToken, `{"incident":{"policy_name":"Disk","started_at":`+value+`}}`)
			require.Equal(t, http.StatusBadRequest, response.status, svc.messages())
			require.Empty(t, svc.messages())
		})
	}
}

// TestCloudMonitoringThrottleCarriesRetryAfter gives the sender a retry hint.
func TestCloudMonitoringThrottleCarriesRetryAfter(t *testing.T) {
	resetThrottleConfig(t)
	for _, key := range throttleConfigKeys {
		gconfig.Shared.Set(key, 1)
	}
	NewTelegram(context.Background(), nil)
	svc := new(webhookTelegram)
	server := startWebhook(t, svc)
	var throttled *webhookResponse
	for range 5 {
		response := postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody)
		if response.status == http.StatusTooManyRequests {
			throttled = &response
		}
	}
	require.NotNil(t, throttled)
	require.Equal(t, "1", throttled.header.Get("Retry-After"))
}

// TestCloudMonitoringWithoutTelegramIsUnavailable keeps a gateway whose Telegram
// service did not start answering a retryable status instead of panicking.
func TestCloudMonitoringWithoutTelegramIsUnavailable(t *testing.T) {
	server := httptest.NewServer(NewCloudMonitoringWebhook(nil))
	t.Cleanup(server.Close)
	for _, username := range []string{"", webhookAlertType} {
		response := postWebhook(t, server.URL, username, webhookToken, openIncidentBody)
		require.Equal(t, http.StatusServiceUnavailable, response.status)
		require.Empty(t, response.header.Get("WWW-Authenticate"))
	}
}

// TestCloudMonitoringBoundsDeliveryTime ends a hung Telegram send with a 503
// instead of holding the request until the sender gives up.
func TestCloudMonitoringBoundsDeliveryTime(t *testing.T) {
	enableAlertThrottle(t)
	svc := &webhookTelegram{send: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	handler := NewCloudMonitoringWebhook(svc)
	handler.deliveryTimeout = 50 * time.Millisecond
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	// The client deadline turns a missing server bound into a failure, not a hang.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(openIncidentBody))
	require.NoError(t, err)
	request.SetBasicAuth(webhookAlertType, webhookToken)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err, "the handler must answer before the sender gives up")
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	require.Empty(t, svc.messages())
}

// TestCloudMonitoringLogsOnlyBoundedMetadata plants a canary in every field the
// handler could log and proves neither the canary nor a huge value reaches a log,
// while a well-formed incident ID still does.
func TestCloudMonitoringLogsOnlyBoundedMetadata(t *testing.T) {
	enableAlertThrottle(t)
	const canary = "SYNTHETIC-LOG-CANARY"
	var output bytes.Buffer
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&output), zap.DebugLevel)
	logger, err := glog.NewWithName("cloud_monitoring_test", glog.LevelDebug,
		zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
	require.NoError(t, err)

	huge := strings.Repeat("9", 512<<10)
	handler := NewCloudMonitoringWebhook(new(webhookTelegram))
	for _, body := range []string{
		`{"incident":{"incident_id":"0.well-formed","state":"open"}}`,
		`{"incident":{"incident_id":"` + canary + ` id","policy_name":"` + canary + `","condition_name":"` + canary +
			`","resource_display_name":"` + canary + `","scoping_project_id":"` + canary + `","url":"` + canary +
			`","summary":"` + canary + `","severity":"` + canary + `","state":"` + canary + `"}}`,
		`{"version":"` + canary + strings.Repeat("v", 512<<10) + `"}`,
		`{"incident":{"started_at":"` + canary + huge + `"}}`,
		`{"incident":{"started_at":` + huge + `}}`,
		`{"incident":{"policy_name":` + huge + `}}`,
		`{"incident":{"policy_name":"` + canary,
	} {
		request := httptest.NewRequest(http.MethodPost, "/webhook/gcp/alerts/cloud-monitoring", strings.NewReader(body))
		request = request.WithContext(gmw.SetLogger(request.Context(), logger))
		request.SetBasicAuth(webhookAlertType, webhookToken)
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
	require.NoError(t, logger.Sync())
	logged := output.String()
	require.Contains(t, logged, `"incident_id":"0.well-formed"`)
	require.Contains(t, logged, "refuse cloud monitoring notification", "the capture must see the decode refusals")
	require.NotContains(t, logged, canary)
	require.Less(t, len(logged), 32<<10, "log volume must not scale with the request body")
}

// TestCloudMonitoringDecodeErrorsNeverQuoteTheBody covers decoder errors the
// incident schema cannot trigger today: encoding/json quotes a number literal
// that overflows an integer field, so a numeric field added later must not
// reopen the leak.
func TestCloudMonitoringDecodeErrorsNeverQuoteTheBody(t *testing.T) {
	huge := strings.Repeat("9", 4096)
	var numeric struct {
		Number int `json:"scoping_project_number"`
	}
	raw := json.Unmarshal([]byte(`{"scoping_project_number":`+huge+`}`), &numeric)
	require.ErrorContains(t, raw, huge, "the probe must reproduce the quoting it guards against")
	described := describeCloudMonitoringDecodeError(raw)
	require.EqualError(t, described, `decode cloud monitoring notification: field "scoping_project_number" has the wrong JSON type`)

	raw = json.Unmarshal([]byte(`{"incident":`+huge), new(cloudMonitoringNotification))
	require.NotContains(t, describeCloudMonitoringDecodeError(raw).Error(), huge)
}
