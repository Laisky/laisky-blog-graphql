package auth

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Results of mcp_auth_decisions_total{result}: one per Authenticate call.
const (
	// decisionAcceptedCache: a key one-api accepted within the positive TTL.
	decisionAcceptedCache = "accepted_cache"
	// decisionAcceptedAuthority: one-api accepted the key in this call (or
	// the shared call this one waited for).
	decisionAcceptedAuthority = "accepted_authority"
	// decisionAcceptedStale: one-api gave no verdict and the key was served
	// from its stale grace window. Any rate means an authority outage.
	decisionAcceptedStale = "accepted_stale"
	// decisionRejectedCache: a key one-api rejected within the negative TTL.
	decisionRejectedCache = "rejected_cache"
	// decisionRejectedAuthority: one-api rejected the key in this call.
	decisionRejectedAuthority = "rejected_authority"
	// decisionInvalid: a malformed or oversized bearer, refused before any
	// cache lookup or upstream call.
	decisionInvalid = "invalid"
	// decisionUnavailable prefixes the unavailable results; the suffix is
	// the UnavailableCause (local_budget, authority, local).
	decisionUnavailable = "unavailable_"
)

// Budgets of mcp_auth_gate_refusals_total{budget}: which of the gate's
// budgets refused a one-api call. A rising count of one budget names the
// setting to raise, or the flood to look for.
const (
	refusalSource      = "per_source"
	refusalGlobal      = "global"
	refusalWindow      = "oneapi_window"
	refusalConcurrency = "concurrency"
)

// Classes of mcp_auth_authority_calls_total{class}.
const (
	callLabelNewKey       = "new_key"
	callLabelRevalidation = "revalidation"
	callLabelBalance      = "balance"
)

// Verdicts of mcp_auth_authority_calls_total{verdict}.
const (
	verdictAccepted  = "accepted"
	verdictRejected  = "rejected"
	verdictNoVerdict = "no_verdict"
)

// authMetrics are the process-wide collectors of the API key authenticator,
// on the default registry the API server serves at /metrics. Suggested alerts
// (docs/manual/mcp.md, Credential Validation, Monitoring):
//
//   - one-api outage:  rate(mcp_auth_decisions_total{result=~"accepted_stale|unavailable_authority"}[5m]) > 0
//   - capacity:        rate(mcp_auth_decisions_total{result="unavailable_local_budget"}[5m]) > 0,
//     with mcp_auth_gate_refusals_total{budget} naming the budget
//   - token-store or mass revocation: a spike of mcp_auth_known_key_rejections_total
//   - shared one-api limit: oneapi_rest_calls_in_window / mcp_auth_oneapi_rate_limit > 0.8
var authMetrics = struct {
	decisions          *prometheus.CounterVec
	gateRefusals       *prometheus.CounterVec
	authorityCalls     *prometheus.CounterVec
	knownKeyRejections prometheus.Counter
	oneapiRateLimit    prometheus.Gauge
}{
	decisions: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_auth_decisions_total",
		Help: "API key authentication decisions, by result (accepted_*, rejected_*, invalid, unavailable_<cause>).",
	}, []string{"result"}),
	gateRefusals: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_auth_gate_refusals_total",
		Help: "one-api calls the API key gate refused to start, by the budget that refused them.",
	}, []string{"budget"}),
	authorityCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_auth_authority_calls_total",
		Help: "Calls the API key gate started to one-api, by class and one-api's verdict.",
	}, []string{"class", "verdict"}),
	knownKeyRejections: prometheus.NewCounter(prometheus.CounterOpts{
		Name: "mcp_auth_known_key_rejections_total",
		Help: "Rejections by one-api of keys it had accepted before: revocations, exhausted quota, or one-api token-store failures (which one-api also answers 401).",
	}),
	oneapiRateLimit: prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "mcp_auth_oneapi_rate_limit",
		Help: "Configured one-api per-IP REST limit per 3 minutes for this process (settings.mcp.auth.validation.oneapi_rate_limit); 0 = exempt.",
	}),
}

func init() {
	prometheus.MustRegister(authMetrics.decisions, authMetrics.gateRefusals, authMetrics.authorityCalls,
		authMetrics.knownKeyRejections, authMetrics.oneapiRateLimit)
}

// observeDecision counts one Authenticate result.
func observeDecision(result string) {
	authMetrics.decisions.WithLabelValues(result).Inc()
}

// observeUnavailable counts one unavailable Authenticate result by cause.
func observeUnavailable(err error) {
	observeDecision(decisionUnavailable + UnavailableCause(err))
}

// observeGateRefusal counts one call the gate refused, by budget.
func observeGateRefusal(budget string) {
	authMetrics.gateRefusals.WithLabelValues(budget).Inc()
}

// observeAuthorityCall counts one call the gate started and its verdict.
func observeAuthorityCall(class string, err error) {
	verdict := verdictNoVerdict
	switch {
	case err == nil:
		verdict = verdictAccepted
	case isCredentialRejection(err):
		verdict = verdictRejected
	}
	authMetrics.authorityCalls.WithLabelValues(class, verdict).Inc()
}
