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
