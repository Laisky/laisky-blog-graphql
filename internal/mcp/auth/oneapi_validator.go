package auth

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"

	errors "github.com/Laisky/errors/v2"
)

// OneAPITokenBalancePath is one-api's read-only, TokenAuth-protected endpoint
// used as the credential authority. It has no billing side effect.
const OneAPITokenBalancePath = "/api/token/balance" //nolint:gosec // G101: a URL path, not a credential

// maxValidationResponseBytes bounds how much of an authority response is drained.
const maxValidationResponseBytes = 4 << 10

// OneAPITokenValidator validates API keys against one-api.
//
// one-api's TokenAuth answers 200 for an enabled, unexpired token with quota;
// 401 for unknown, expired, disabled or quota-exhausted tokens (and, by its
// design, for its own token-store failures); 403 for subnet-restricted tokens
// used from another network or for disabled owners; 429 when its per-IP API
// rate limit is reached; and 5xx for other internal failures.
type OneAPITokenValidator struct {
	endpoint string
	client   *http.Client
}

// NewOneAPITokenValidator targets baseURL+OneAPITokenBalancePath. A nil client
// uses a dedicated client that never follows redirects, so the key is only
// ever sent to the configured authority.
func NewOneAPITokenValidator(baseURL string, client *http.Client) (*OneAPITokenValidator, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, errors.Wrap(err, "parse one-api base url")
	}
	if (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return nil, errors.Errorf("one-api base url must be an absolute http(s) url, got %q", parsed.Redacted())
	}
	if client == nil {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}

	return &OneAPITokenValidator{
		endpoint: strings.TrimSuffix(parsed.String(), "/") + OneAPITokenBalancePath,
		client:   client,
	}, nil
}

// Endpoint returns the validation URL, for wiring checks.
func (v *OneAPITokenValidator) Endpoint() string {
	return v.endpoint
}

// ValidateAPIKey asks one-api whether the key is usable.
func (v *OneAPITokenValidator) ValidateAPIKey(ctx context.Context, apiKey string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, v.endpoint, nil)
	if err != nil {
		return errors.Wrap(errors.Join(ErrValidationUnavailable, err), "build one-api validation request")
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Accept", "application/json")

	response, err := v.client.Do(request)
	if err != nil {
		return errors.Wrap(errors.Join(ErrValidationUnavailable, err), "call one-api token validation")
	}
	defer func() { _ = response.Body.Close() }()
	// Drain a bounded amount so the connection can be reused; the verdict is the status.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxValidationResponseBytes))

	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return nil
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return errors.Wrapf(ErrCredentialRejected, "one-api refused the api key with status %d", response.StatusCode)
	default:
		return errors.Wrapf(ErrValidationUnavailable, "one-api token validation returned status %d", response.StatusCode)
	}
}
