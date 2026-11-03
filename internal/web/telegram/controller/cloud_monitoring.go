package telegram

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
)

const (
	// maxCloudMonitoringBodyBytes bounds one webhook body; a larger body is
	// refused rather than truncated.
	maxCloudMonitoringBodyBytes = 1 << 20
	// cloudMonitoringChallenge is the challenge Cloud Monitoring requires before
	// it sends webhook_basicauth credentials.
	cloudMonitoringChallenge = `Basic realm="telegram-alerts", charset="UTF-8"`
	// cloudMonitoringEnvParam names the optional deployment label in the webhook URL.
	cloudMonitoringEnvParam = "env"
)

// cloudMonitoringEnvPattern bounds the deployment label shown in each alert.
var cloudMonitoringEnvPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// cloudMonitoringNotification is the body Cloud Monitoring posts for one incident change.
type cloudMonitoringNotification struct {
	Version  string                   `json:"version"`
	Incident *cloudMonitoringIncident `json:"incident"`
}

// cloudMonitoringIncident is the subset of a Cloud Monitoring incident shown to subscribers.
type cloudMonitoringIncident struct {
	IncidentID          string           `json:"incident_id"`
	ScopingProjectID    string           `json:"scoping_project_id"`
	URL                 string           `json:"url"`
	State               string           `json:"state"`
	StartedAt           cloudMonitorTime `json:"started_at"`
	EndedAt             cloudMonitorTime `json:"ended_at"`
	Summary             string           `json:"summary"`
	PolicyName          string           `json:"policy_name"`
	ConditionName       string           `json:"condition_name"`
	Severity            string           `json:"severity"`
	ResourceDisplayName string           `json:"resource_display_name"`
}

// cloudMonitorTime is a Unix timestamp sent as a JSON number, a numeric string
// or null. Zero means absent, which is how an open incident reports ended_at.
type cloudMonitorTime int64

// UnmarshalJSON accepts a number, a numeric string or null and returns an error
// for any other value.
func (t *cloudMonitorTime) UnmarshalJSON(raw []byte) error {
	value := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if value == "" || value == "null" {
		*t = 0
		return nil
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return errors.Wrapf(err, "parse unix timestamp %q", value)
	}
	*t = cloudMonitorTime(int64(seconds))
	return nil
}

// utc returns the timestamp as UTC, or the zero time when it is absent.
func (t cloudMonitorTime) utc() time.Time {
	if t <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(t), 0).UTC()
}

// decodeCloudMonitoringIncident extracts the incident from one webhook body and
// returns an error when the body is not a notification carrying an incident.
// The version is not checked because the console's test notification and the
// published 1.2 schema both carry the fields this receiver reads.
func decodeCloudMonitoringIncident(body []byte) (*cloudMonitoringIncident, error) {
	var notification cloudMonitoringNotification
	if err := json.Unmarshal(body, &notification); err != nil {
		return nil, errors.Wrap(err, "decode cloud monitoring notification")
	}
	if notification.Incident == nil {
		return nil, errors.Errorf("cloud monitoring notification %q carries no incident", notification.Version)
	}
	return notification.Incident, nil
}

// message renders the incident as plain text. env labels the deployment and
// falls back to the scoping project. State, severity and policy come first so
// the notification preview alone is actionable.
func (i *cloudMonitoringIncident) message(env string) string {
	state := "OPEN"
	if strings.EqualFold(strings.TrimSpace(i.State), "closed") {
		state = "RESOLVED"
	}
	severity := strings.ToUpper(strings.TrimSpace(i.Severity))
	if severity == "" || severity == "NO SEVERITY" {
		severity = "UNSPECIFIED"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s %s: %s",
		strings.ToUpper(cmp.Or(env, strings.TrimSpace(i.ScopingProjectID), "unknown")),
		state, severity, cmp.Or(strings.TrimSpace(i.PolicyName), "unnamed policy"))
	if condition := strings.TrimSpace(i.ConditionName); condition != "" {
		fmt.Fprintf(&b, "\ncondition: %s", condition)
	}
	if resource := strings.TrimSpace(i.ResourceDisplayName); resource != "" {
		fmt.Fprintf(&b, "\nresource: %s", resource)
	}
	if summary := strings.TrimSpace(i.Summary); summary != "" {
		fmt.Fprintf(&b, "\n%s", summary)
	}
	if started := i.StartedAt.utc(); !started.IsZero() {
		fmt.Fprintf(&b, "\nstarted: %s", started.Format(time.RFC3339))
	}
	if ended := i.EndedAt.utc(); !ended.IsZero() {
		fmt.Fprintf(&b, "\nended: %s", ended.Format(time.RFC3339))
	}
	if url := strings.TrimSpace(i.URL); url != "" {
		fmt.Fprintf(&b, "\n%s", url)
	}
	return b.String()
}

// CloudMonitoringWebhook turns Cloud Monitoring incidents into Telegram monitor
// alerts, so a monitoring policy reaches subscribers without a translating relay.
// The HTTP Basic username is the alert type and the password is its push token.
type CloudMonitoringWebhook struct {
	svc    service.Interface
	sender *MutationResolver
}

// NewCloudMonitoringWebhook returns a webhook that authenticates and delivers
// through svc, which must not be nil.
func NewCloudMonitoringWebhook(svc service.Interface) *CloudMonitoringWebhook {
	return &CloudMonitoringWebhook{svc: svc, sender: NewMutationResolver(svc)}
}

// ServeHTTP authenticates one notification, then delivers it through the same
// path as TelegramMonitorAlert. Credentials are checked before the body is read
// and before the alert throttle is consulted, so an unauthenticated caller can
// neither cost a body allocation nor spend an alert type's send budget. Only a
// status is returned, so no alert content or credential can echo back.
func (h *CloudMonitoringWebhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := gmw.GetLogger(ctx).Named("cloud_monitoring_webhook")

	alertType, token, ok := r.BasicAuth()
	if !ok {
		w.Header().Set("WWW-Authenticate", cloudMonitoringChallenge)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if _, err := h.svc.ValidateTokenForAlertType(ctx, token, alertType); err != nil {
		if errors.Is(err, service.ErrAlertUnauthorized) {
			logger.Debug("refuse cloud monitoring credentials")
			w.Header().Set("WWW-Authenticate", cloudMonitoringChallenge)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		logger.Error("check cloud monitoring credentials", zap.Error(err))
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	env := r.URL.Query().Get(cloudMonitoringEnvParam)
	if env != "" && !cloudMonitoringEnvPattern.MatchString(env) {
		logger.Warn("refuse invalid cloud monitoring env label", zap.Int("env_bytes", len(env)))
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCloudMonitoringBodyBytes))
	if err != nil {
		logger.Warn("read cloud monitoring notification", zap.Error(err))
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	incident, err := decodeCloudMonitoringIncident(body)
	if err != nil {
		logger.Warn("refuse cloud monitoring notification", zap.Error(err), zap.Int("body_bytes", len(body)))
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	logger = logger.With(zap.String("alert_type", alertType), zap.String("incident_id", incident.IncidentID),
		zap.String("policy", incident.PolicyName), zap.String("state", incident.State))
	_, err = h.sender.TelegramMonitorAlert(ctx, alertType, token, incident.message(env))
	switch {
	case err == nil:
		logger.Info("forwarded cloud monitoring incident")
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, errAlertThrottled):
		logger.Warn("cloud monitoring incident throttled", zap.Error(err))
		w.WriteHeader(http.StatusTooManyRequests)
	default:
		logger.Error("forward cloud monitoring incident", zap.Error(err))
		var fanout *AlertFanoutError
		if errors.As(err, &fanout) && fanout.RetryAfterSeconds > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(fanout.RetryAfterSeconds))
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}
}
