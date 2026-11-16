package auth

import (
	"net/url"
	"strings"
	"time"
)

// isHTTPScheme reports whether a lower-case URL scheme is http or https, the
// only schemes an authority or public origin may use.
func isHTTPScheme(scheme string) bool {
	return scheme == "https" || scheme == "http"
}

// authorityBaseURLReporter is implemented by validators that consult a
// network authority; AuthorityBaseURL returns the authority's base URL, the
// prefix of its /api/... routes.
type authorityBaseURLReporter interface {
	AuthorityBaseURL() string
}

// canonicalBaseURL returns "scheme://host[:port][/path]" of an absolute
// http(s) URL, with scheme and host in lower case and trailing slashes
// removed, or "" for anything else (including a query or fragment). Ports
// are compared literally, so an explicit default port names a different
// authority (a conservative mismatch), and so does a different path prefix.
func canonicalBaseURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if !isHTTPScheme(scheme) || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	return scheme + "://" + strings.ToLower(parsed.Host) + strings.TrimRight(parsed.EscapedPath(), "/")
}

// ObservesAuthority reports whether calls under baseURL are calls to this
// Authenticator's credential authority, so that BeginAuthorityCall can use
// their verdicts. baseURL is the one-api base the call was built from (for a
// consume, oneapi.BillingAPI), compared with the validator's base including
// any path prefix. Validators without a network authority never match.
func (a *Authenticator) ObservesAuthority(baseURL string) bool {
	if a == nil || a.authority == "" {
		return false
	}
	return canonicalBaseURL(baseURL) == a.authority
}

// BeginAuthorityCall prepares to observe one call that the caller makes to
// the credential authority under baseURL on behalf of apiKey, outside this
// Authenticator — a one-api consume, which one-api serves behind the same
// TokenAuth as the validation endpoint. Call the returned function once the
// call completes, with whether the authority accepted the key.
//
// An acceptance restarts the trust period of a key this Authenticator already
// accepted, from the moment the call started. Actively used keys therefore
// need no separate revalidation call, so the gate's budgets only have to cover
// keys that make no such call within PositiveTTL, and the revocation lag
// stays at most PositiveTTL after the authority's last acceptance. Nothing
// changes for another authority, an unknown or rejected key, a refusal, or an
// acceptance older than the recorded one: refusals keep coming from the
// validator alone, so a consume refused for quota reasons never locks a key out.
func (a *Authenticator) BeginAuthorityCall(baseURL, apiKey string) func(accepted bool) {
	if !a.ObservesAuthority(baseURL) {
		return func(bool) {}
	}
	candidate, err := a.parseCandidate("Bearer " + apiKey)
	if err != nil {
		return func(bool) {}
	}
	key, startedAt := candidate.APIKeyHash, a.opts.Now()
	return func(accepted bool) {
		if accepted {
			a.refreshTrust(key, startedAt)
		}
	}
}

// refreshTrust moves a known key's last acceptance forward to acceptedAt.
// It runs under the verdict lock, so it can never resurrect a key whose
// rejection was recorded in the meantime (a rejection removes the entry).
func (a *Authenticator) refreshTrust(key string, acceptedAt time.Time) {
	a.verdicts.Lock()
	defer a.verdicts.Unlock()

	now := a.opts.Now()
	entry, known := a.positive.get(key, now)
	if !known || !acceptedAt.After(entry.validatedAt) {
		return
	}
	if _, rejected := a.negative.get(key, now); rejected {
		return
	}
	a.positive.put(key, positiveEntry{validatedAt: acceptedAt}, a.positiveExpiry(acceptedAt))
}
