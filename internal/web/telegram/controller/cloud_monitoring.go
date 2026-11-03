package telegram

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
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
	// cloudMonitoringRefusalBurst and cloudMonitoringRefusalsPerSecond bound the
	// credential lookups that do not authenticate, across all callers, since
	// Google publishes no sender addresses to key a budget by. Accepted
	// credentials and the credential-free first request are not counted, so the
	// bound only reaches a correctly configured channel while someone else is
	// guessing. Per-address limits belong at the edge proxy, which sees them.
	cloudMonitoringRefusalBurst      = 20
	cloudMonitoringRefusalsPerSecond = 5
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
	refusals        *rate.Limiter
	deliveryTimeout time.Duration
}

// NewCloudMonitoringWebhook returns a webhook that authenticates and delivers
// through svc. A nil svc, meaning the Telegram service did not start, makes
// every request answer 503, so the sender keeps retrying instead of treating
// the route as gone.
func NewCloudMonitoringWebhook(svc service.Interface) *CloudMonitoringWebhook {
	return &CloudMonitoringWebhook{
		svc:             svc,
		sender:          NewMutationResolver(svc),
		refusals:        rate.NewLimiter(cloudMonitoringRefusalsPerSecond, cloudMonitoringRefusalBurst),
		deliveryTimeout: cloudMonitoringDeliveryTimeout,
	}
}

// ServeHTTP authenticates one notification, then delivers it through the same
// subscriber path as TelegramMonitorAlert. Credentials are checked once, before
// the body is read and before the alert throttle is consulted, so an
// unauthenticated caller can neither cost a body allocation nor spend an alert
// type's send budget, and the refusal budget bounds how often such a caller can
// reach storage. Only a status is returned, so no alert content or credential
// can echo back.
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
	if wait := h.refusalWait(); wait > 0 {
		logger.Warn("defer cloud monitoring credential check after repeated refusals", zap.Duration("wait", wait))
		w.Header().Set("Retry-After", retryAfterSeconds(wait))
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	alert, err := h.svc.ValidateTokenForAlertType(ctx, token, alertType)
	if err == nil && alert == nil {
		err = errors.New("telegram alert authorization returned no alert")
	}
	if err != nil {
		// Reserve charges the refusal even when concurrent refusals overdrew the
		// budget, so the overdraft is repaid before the next lookup is allowed.
		h.refusals.Reserve()
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

// refusalWait returns how long until the refusal budget allows another
// credential lookup, or zero when it allows one now. It spends nothing; only a
// lookup that does not authenticate is charged.
func (h *CloudMonitoringWebhook) refusalWait() time.Duration {
	tokens := h.refusals.Tokens()
	if tokens >= 1 {
		return 0
	}
	return time.Duration((1 - tokens) / float64(h.refusals.Limit()) * float64(time.Second))
}

// retryAfterSeconds renders wait as a Retry-After value of at least one second.
func retryAfterSeconds(wait time.Duration) string {
	return strconv.Itoa(max(1, int(math.Ceil(wait.Seconds()))))
}
