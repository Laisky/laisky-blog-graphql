package auth

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	errors "github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/library/httpclient"
)

// OneAPITokenBalancePath is one-api's read-only, TokenAuth-protected endpoint
// used as the credential authority. It has no billing side effect.
const OneAPITokenBalancePath = "/api/token/balance" //nolint:gosec // G101: a URL path, not a credential

// maxValidationResponseBytes bounds how much of an authority response is read.
const maxValidationResponseBytes = 64 << 10

// Balance is a key's one-api quota, as reported by /api/token/balance.
type Balance struct {
	RemainQuota    float64 `json:"remain_quota"`
	UsedQuota      float64 `json:"used_quota"`
	UnlimitedQuota bool    `json:"unlimited_quota"`
}

// BalanceValidator is a Validator that also reports the key's balance in the
// same authority call.
type BalanceValidator interface {
	Validator
	// FetchBalance validates apiKey like ValidateAPIKey and returns its quota.
	FetchBalance(ctx context.Context, apiKey string) (Balance, error)
}

// OneAPITokenValidator validates API keys against one-api.
//
// one-api's TokenAuth answers 401 for unknown, expired, disabled or
// quota-exhausted tokens (and, by its design, for its own token-store
// failures), 403 for subnet-restricted tokens used from another network or for
// disabled owners, 429 when its per-IP API rate limit is reached and 5xx for
// other internal failures. A 200 is accepted only as a JSON body with
// "success": true: one-api reports some controller failures as
// 200 {"success": false}, and its SPA answers 200 HTML outside /api.
type OneAPITokenValidator struct {
	endpoint string
	client   *http.Client
}

// NewOneAPITokenValidator targets baseURL+OneAPITokenBalancePath. Whatever
// client is injected (nil uses a zero-value one), the validator works on a
// copy that never follows redirects: a redirect answer is no verdict
// (ErrValidationUnavailable), so the key is only ever sent to the configured
// authority and no other origin can answer for it. The caller's client is not
// modified.
func NewOneAPITokenValidator(baseURL string, client *http.Client) (*OneAPITokenValidator, error) {
	parsed, err := parseAuthorityURL(baseURL)
	if err != nil {
		return nil, errors.Wrap(err, "one-api base url")
	}
	return &OneAPITokenValidator{
		endpoint: strings.TrimSuffix(parsed.String(), "/") + OneAPITokenBalancePath,
		client:   httpclient.WithoutRedirects(client),
	}, nil
}

// NewOneAPITokenValidatorAt targets the exact endpoint URL, which must answer
// like one-api's /api/token/balance. It keeps deployments of the deprecated
// settings.oneapi.balance_url working when their proxy serves the endpoint
// under another path. Redirects are refused as for NewOneAPITokenValidator.
func NewOneAPITokenValidatorAt(endpoint string, client *http.Client) (*OneAPITokenValidator, error) {
	parsed, err := parseAuthorityURL(endpoint)
	if err != nil {
		return nil, errors.Wrap(err, "one-api validation endpoint")
	}
	return &OneAPITokenValidator{endpoint: parsed.String(), client: httpclient.WithoutRedirects(client)}, nil
}

// parseAuthorityURL accepts only an absolute http(s) URL without user info,
// so the key is only ever sent where the configuration names.
func parseAuthorityURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, errors.Wrap(err, "parse url")
	}
	if !isHTTPScheme(strings.ToLower(parsed.Scheme)) || parsed.Host == "" || parsed.User != nil {
		return nil, errors.Errorf("must be an absolute http(s) url without user info, got %q", parsed.Redacted())
	}
	return parsed, nil
}

// AuthorityBaseURL returns the one-api base URL the validator calls under,
// which Authenticator.BeginAuthorityCall matches against other
// TokenAuth-protected calls (one-api consumes), or "" when the endpoint does
// not end in OneAPITokenBalancePath and so names no base.
func (v *OneAPITokenValidator) AuthorityBaseURL() string {
	base, ok := strings.CutSuffix(v.endpoint, OneAPITokenBalancePath)
	if !ok {
		return ""
	}
	return canonicalBaseURL(base)
}

// Endpoint returns the validation URL, for wiring checks.
func (v *OneAPITokenValidator) Endpoint() string {
	return v.endpoint
}

// ValidateAPIKey asks one-api whether the key is usable.
func (v *OneAPITokenValidator) ValidateAPIKey(ctx context.Context, apiKey string) error {
	_, err := v.FetchBalance(ctx, apiKey)
	return errors.WithStack(err)
}

// FetchBalance asks one-api whether the key is usable and returns its quota.
func (v *OneAPITokenValidator) FetchBalance(ctx context.Context, apiKey string) (Balance, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, v.endpoint, nil)
	if err != nil {
		return Balance{}, errors.Wrap(errors.Join(ErrValidationUnavailable, err), "build one-api validation request")
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Accept", "application/json")

	response, err := v.client.Do(request)
	if err != nil {
		return Balance{}, errors.Wrap(errors.Join(ErrValidationUnavailable, err), "call one-api token validation")
	}
	defer func() { _ = response.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxValidationResponseBytes+1))

	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return Balance{}, errors.Wrapf(ErrCredentialRejected, "one-api refused the api key with status %d", response.StatusCode)
	case response.StatusCode != http.StatusOK:
		return Balance{}, errors.Wrapf(ErrValidationUnavailable, "one-api token validation returned status %d", response.StatusCode)
	case readErr != nil:
		return Balance{}, errors.Wrap(errors.Join(ErrValidationUnavailable, readErr), "read one-api token validation")
	}
	return decodeBalance(response.Header.Get("Content-Type"), body)
}

// decodeBalance accepts only a bounded JSON envelope with "success": true;
// anything else means one-api gave no verdict.
func decodeBalance(contentType string, body []byte) (Balance, error) {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "application/json" {
		return Balance{}, errors.Wrapf(ErrValidationUnavailable, "one-api answered 200 with content type %q", contentType)
	}
	if len(body) > maxValidationResponseBytes {
		return Balance{}, errors.Wrap(ErrValidationUnavailable, "one-api answer exceeds the size limit")
	}
	var envelope struct {
		Success bool    `json:"success"`
		Data    Balance `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return Balance{}, errors.Wrap(errors.Join(ErrValidationUnavailable, err), "decode one-api answer")
	}
	if !envelope.Success {
		return Balance{}, errors.Wrap(ErrValidationUnavailable, "one-api answered 200 without success")
	}
	return envelope.Data, nil
}
