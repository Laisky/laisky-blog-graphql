package telegram

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"golang.org/x/time/rate"

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
	// cloudMonitoringRefusalLogInterval spaces the warnings about refused
	// credentials, so a guessing caller cannot set the log volume.
	cloudMonitoringRefusalLogInterval = time.Minute
	// cloudMonitoringDeliveryTimeout bounds the fan-out to subscribers, so a slow
	// Telegram answer ends in a 503 the sender can retry rather than in a request
	// the sender or a proxy has already given up on. Google publishes no webhook
	// timeout; this stays well under Cloudflare's 125-second origin read timeout.
	cloudMonitoringDeliveryTimeout = 20 * time.Second
	// maxCloudMonitoringUnixSeconds is 9999-12-31T23:59:59Z, the last instant an
	// RFC 3339 timestamp can show.
	maxCloudMonitoringUnixSeconds = 253402300799
	// maxLoggedFieldBytes bounds a decoder-provided field path in a log entry.
	maxLoggedFieldBytes = 64
	// cloudMonitoringTestVersion is the version of the console's Test Connection
	// payload, which otherwise looks like an open incident.
	cloudMonitoringTestVersion = "test"
)

var (
	// cloudMonitoringEnvPattern bounds the deployment label shown in each alert.
	cloudMonitoringEnvPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	// cloudMonitoringIncidentIDPattern matches the incident IDs that are logged;
	// any other value is logged by its length only.
	cloudMonitoringIncidentIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	// cloudMonitoringSecondsPattern is the only spelling a timestamp may use:
	// decimal Unix seconds with an optional fraction, and no sign or exponent.
	cloudMonitoringSecondsPattern = regexp.MustCompile(`^[0-9]{1,12}(\.[0-9]{1,9})?$`)
	// errCloudMonitoringTimestamp rejects a timestamp outside the documented forms.
	// It never quotes the value, which can be most of the body.
	errCloudMonitoringTimestamp = errors.New("timestamp is not null or Unix seconds up to year 9999")
)

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
	// test marks the console's Test Connection payload.
	test bool
}

// cloudMonitorTime is a Unix timestamp in seconds. Zero means absent, which is
// how an open incident reports ended_at.
type cloudMonitorTime int64

// UnmarshalJSON accepts null, an empty string, or non-negative decimal seconds
// sent as a JSON number or string, and returns errCloudMonitoringTimestamp for
// anything else, including values past year 9999. A fraction is truncated.
func (t *cloudMonitorTime) UnmarshalJSON(raw []byte) error {
	value := string(raw)
	switch {
	case value == "null":
		*t = 0
		return nil
	case strings.HasPrefix(value, `"`):
		if err := json.Unmarshal(raw, &value); err != nil {
			return errors.WithStack(errCloudMonitoringTimestamp)
		}
		if value == "" {
			*t = 0
			return nil
		}
	}
	if !cloudMonitoringSecondsPattern.MatchString(value) {
		return errors.WithStack(errCloudMonitoringTimestamp)
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || seconds > maxCloudMonitoringUnixSeconds {
		return errors.WithStack(errCloudMonitoringTimestamp)
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
// The version is not required, because the console's Test Connection payload
// ("test") and the published 1.2 schema both carry the fields this receiver
// reads; it only marks a test. The error never quotes the body, so it is safe
// to log.
func decodeCloudMonitoringIncident(body []byte) (*cloudMonitoringIncident, error) {
	var notification cloudMonitoringNotification
	if err := json.Unmarshal(body, &notification); err != nil {
		return nil, describeCloudMonitoringDecodeError(err)
	}
	if notification.Incident == nil {
		return nil, errors.New("cloud monitoring notification carries no incident")
	}
	notification.Incident.test = strings.EqualFold(strings.TrimSpace(notification.Version), cloudMonitoringTestVersion)
	return notification.Incident, nil
}

// describeCloudMonitoringDecodeError keeps the decoder's diagnosis without the
// body. encoding/json quotes an offending number literal, and either that or a
// timestamp can be most of a 1 MiB body.
func describeCloudMonitoringDecodeError(err error) error {
	var syntax *json.SyntaxError
	var mismatch *json.UnmarshalTypeError
	switch {
	case errors.Is(err, errCloudMonitoringTimestamp):
		return errors.WithStack(errCloudMonitoringTimestamp)
	case errors.As(err, &syntax):
		return errors.Errorf("decode cloud monitoring notification: invalid JSON at byte %d", syntax.Offset)
	case errors.As(err, &mismatch):
		return errors.Errorf("decode cloud monitoring notification: field %q has the wrong JSON type", truncateBytes(mismatch.Field, maxLoggedFieldBytes))
	default:
		return errors.New("decode cloud monitoring notification: invalid body")
	}
}

// truncateBytes returns at most limit bytes of s.
func truncateBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}

// stateLabel names the incident change. Cloud Monitoring documents only open
// and closed, in either case; any other state is shown as unknown instead of as
// an opening. The Test Connection payload claims to be open, so it is labeled
// TEST to keep a channel check from reading as a real incident.
func (i *cloudMonitoringIncident) stateLabel() string {
	if i.test {
		return "TEST"
	}
	switch strings.ToLower(strings.TrimSpace(i.State)) {
	case "open":
		return "OPEN"
	case "closed":
		return "RESOLVED"
	default:
		return "UNKNOWN-STATE"
	}
}

// message renders the incident as plain text. env labels the deployment and
// falls back to the scoping project. State, severity and policy come first so
// the notification preview alone is actionable.
func (i *cloudMonitoringIncident) message(env string) string {
	severity := strings.ToUpper(strings.TrimSpace(i.Severity))
	if severity == "" || severity == "NO SEVERITY" {
		severity = "UNSPECIFIED"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s %s: %s",
		strings.ToUpper(cmp.Or(env, strings.TrimSpace(i.ScopingProjectID), "unknown")),
		i.stateLabel(), severity, cmp.Or(strings.TrimSpace(i.PolicyName), "unnamed policy"))
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

// logFields returns the incident metadata a log may carry: the incident ID when
// it looks like one, and the derived state. Policy, resource and summary text
// are alert content and are never logged.
func (i *cloudMonitoringIncident) logFields() []zap.Field {
	fields := []zap.Field{zap.String("state", i.stateLabel())}
	if cloudMonitoringIncidentIDPattern.MatchString(i.IncidentID) {
		return append(fields, zap.String("incident_id", i.IncidentID))
	}
	return append(fields, zap.Int("incident_id_bytes", len(i.IncidentID)))
}

// CloudMonitoringWebhook turns Cloud Monitoring incidents into Telegram monitor
// alerts, so a monitoring policy reaches subscribers without a translating relay.
// The HTTP Basic username is the alert type and the password is its push token.
type CloudMonitoringWebhook struct {
	svc             service.Interface
	sender          *MutationResolver
	credentials     *alertCredentialDirectory
	deliveryTimeout time.Duration
	refusals        atomic.Int64
	refusalLog      rate.Sometimes
}

// NewCloudMonitoringWebhook returns a webhook that authenticates and delivers
// through svc. A nil svc, meaning the Telegram service did not start, makes
// every request answer 503, so the sender keeps retrying instead of treating
// the route as gone.
func NewCloudMonitoringWebhook(svc service.Interface) *CloudMonitoringWebhook {
	h := &CloudMonitoringWebhook{
		svc:             svc,
		sender:          NewMutationResolver(svc),
		deliveryTimeout: cloudMonitoringDeliveryTimeout,
		refusalLog:      rate.Sometimes{Interval: cloudMonitoringRefusalLogInterval},
	}
	if svc != nil {
		h.credentials = newAlertCredentialDirectory(svc.LoadAlertCredentials, maxMirroredAlertCredentials, alertCredentialRefreshInterval)
	}
	return h
}

// ServeHTTP authenticates one notification, then delivers it through the same
// subscriber path as TelegramMonitorAlert. Credentials are checked before the
// body is read and before the alert throttle is consulted, so an
// unauthenticated caller can neither cost a body allocation nor spend an alert
// type's send budget. The credential directory refuses an unknown credential
// without a storage lookup, so guessing has no budget to spend and cannot lock
// out a valid channel; a known one is checked against storage once. Only a
// status is returned, so no alert content or credential can echo back.
func (h *CloudMonitoringWebhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := gmw.GetLogger(ctx).Named("cloud_monitoring_webhook")
	if h.svc == nil {
		logger.Warn("refuse cloud monitoring notification: telegram service is not running")
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	alertType, token, ok := r.BasicAuth()
	if !ok {
		w.Header().Set("WWW-Authenticate", cloudMonitoringChallenge)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch h.credentials.check(ctx, logger, alertType, token) {
	case credentialUnknown:
		h.logRefusal(logger)
		w.Header().Set("WWW-Authenticate", cloudMonitoringChallenge)
		w.WriteHeader(http.StatusUnauthorized)
		return
	case credentialUnverifiable:
		// The directory logs why when it reads; logging here would let a
		// guessing caller set the log volume.
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	case credentialKnown:
	}
	alert, err := h.svc.ValidateTokenForAlertType(ctx, token, alertType)
	if err == nil && alert == nil {
		err = errors.New("telegram alert authorization returned no alert")
	}
	if err != nil {
		if errors.Is(err, service.ErrAlertUnauthorized) {
			// Storage changed since the directory read, as when a token is rotated.
			h.credentials.forget(alertType, token)
			h.logRefusal(logger)
			w.Header().Set("WWW-Authenticate", cloudMonitoringChallenge)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		logger.Error("check cloud monitoring credentials", zap.Error(err))
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	logger = logger.With(zap.String("alert_type", alert.Name))

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
	logger = logger.With(incident.logFields()...)

	complete, err := composeAlert(alert.Name, incident.message(env))
	if err != nil {
		logger.Warn("refuse cloud monitoring incident text", zap.Error(err))
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err = admitAlert(alert.Name); err != nil { //nolint:contextcheck // Rate admission does not need request context.
		if errors.Is(err, errAlertThrottled) {
			logger.Warn("cloud monitoring incident throttled", zap.Error(err))
			// Every throttle bucket refills at least one send per second.
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		logger.Error("admit cloud monitoring incident", zap.Error(err))
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	deliverCtx, cancel := context.WithTimeout(ctx, h.deliveryTimeout)
	defer cancel()
	if _, err = h.sender.deliverAuthorizedAlert(deliverCtx, alert, complete); err != nil {
		logger.Error("forward cloud monitoring incident", zap.Error(err))
		var fanout *AlertFanoutError
		if errors.As(err, &fanout) && fanout.RetryAfterSeconds > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(fanout.RetryAfterSeconds))
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	logger.Info("forwarded cloud monitoring incident")
	w.WriteHeader(http.StatusNoContent)
}

// logRefusal counts one refused credential and warns at most once per
// cloudMonitoringRefusalLogInterval, so a misconfigured channel or a guessing
// caller leaves a steady, alertable signal without one entry per request.
func (h *CloudMonitoringWebhook) logRefusal(logger glog.Logger) {
	total := h.refusals.Add(1)
	h.refusalLog.Do(func() {
		logger.Warn("refuse cloud monitoring credentials", zap.Int64("refused_total", total))
	})
}
