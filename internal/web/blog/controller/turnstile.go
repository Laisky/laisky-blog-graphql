package controller

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	gconfig "github.com/Laisky/go-config/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	"github.com/Laisky/laisky-blog-graphql/library/clientip"
)

const (
	turnstileVerifyEndpoint    = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	turnstileTokenLengthLimit  = 5000
	turnstileVerifyHTTPTimeout = 8 * time.Second
)

var turnstileVerifyHTTPClient = &http.Client{
	Timeout: turnstileVerifyHTTPTimeout,
}

// turnstileVerifyResult describes the response payload from Cloudflare Turnstile verification API.
// Parameters: The fields map JSON response keys returned by the siteverify endpoint.
// Returns: The struct is used for decoding verification responses.
type turnstileVerifyResult struct {
	Success    bool     `json:"success"`
	ErrorCodes []string `json:"error-codes"`
}

// turnstileGateError converts a Turnstile gate failure into the stable client
// error. It accepts the gate error and returns ErrTurnstileRequired when the
// client must show a challenge, or ErrTurnstileFailed when a supplied token
// could not be verified. A gate failure never reports the credentials as wrong.
func turnstileGateError(err error) error {
	if errors.Is(err, model.ErrLoginUnavailable) {
		return errors.WithStack(model.ErrLoginUnavailable)
	}
	if errors.Is(err, model.ErrTurnstileRequired) {
		return errors.WithStack(model.ErrTurnstileRequired)
	}
	return errors.WithStack(model.ErrTurnstileFailed)
}

// verifyTurnstileToken verifies a Turnstile token with Cloudflare siteverify endpoint.
// Parameters: ctx controls request lifecycle, secret is the Turnstile secret key, token is the challenge token, remoteIP is the optional client IP.
// Returns: Nil when verification succeeds; otherwise returns a wrapped error.
func verifyTurnstileToken(ctx context.Context, secret string, token string, remoteIP string) error {
	form := url.Values{}
	form.Set("secret", secret)
	form.Set("response", token)
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, turnstileVerifyEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return errors.Wrap(err, "create turnstile verify request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := turnstileVerifyHTTPClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "request turnstile verify endpoint")
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return errors.Errorf("turnstile verify endpoint returned status %d", resp.StatusCode)
	}

	var result turnstileVerifyResult
	if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return errors.Wrap(err, "decode turnstile verify response")
	}

	if !result.Success {
		return errors.Errorf("turnstile verification rejected: %s", strings.Join(result.ErrorCodes, ","))
	}

	return nil
}

// resolveTurnstileRequestHost extracts the normalized request host from context.
// Parameters: ctx carries optional Gin request context.
// Returns: The X-Forwarded-Host reported by a trusted proxy, else the Host header, normalized; empty when unavailable.
func resolveTurnstileRequestHost(ctx context.Context) string {
	gctx, ok := gmw.GetGinCtxFromStdCtx(ctx)
	if !ok || gctx == nil || gctx.Request == nil {
		return ""
	}

	return normalizeTurnstileHost(clientip.RequestHost(gctx.Request))
}

// resolveTurnstileClientIP returns the client IP sent to siteverify as remoteip.
// Parameters: ctx carries the request context.
// Returns: The canonical client IP resolved by the shared trusted-proxy policy, or empty string when unavailable.
func resolveTurnstileClientIP(ctx context.Context) string {
	return clientip.FromContext(ctx)
}

// isTurnstileHostMatched checks whether the request host matches the configured hosts for a site.
// Parameters: siteBaseKey is the site config key prefix, requestHost is the normalized request host.
// Returns: True when the host matches either `hosts` or `host` fields.
func isTurnstileHostMatched(siteBaseKey string, requestHost string) bool {
	hosts := normalizeTurnstileHostList(gconfig.Shared.GetStringSlice(siteBaseKey + ".hosts"))
	if len(hosts) == 0 {
		host := normalizeTurnstileHost(gconfig.Shared.GetString(siteBaseKey + ".host"))
		if host != "" {
			hosts = []string{host}
		}
	}

	for _, host := range hosts {
		if host == requestHost {
			return true
		}
	}

	return false
}

// normalizeTurnstileHostList normalizes hostnames and removes empty items.
// Parameters: hosts is the configured host list.
// Returns: A normalized host slice that can be used for comparisons.
func normalizeTurnstileHostList(hosts []string) []string {
	result := make([]string, 0, len(hosts))
	for _, host := range hosts {
		normalized := normalizeTurnstileHost(host)
		if normalized == "" {
			continue
		}
		result = append(result, normalized)
	}

	return result
}

// normalizeTurnstileHost normalizes hostname by trimming spaces, lowercasing, and dropping ports.
// Parameters: rawHost is the input host value that may include port or trailing dot.
// Returns: The normalized host string, or empty string when input is blank.
func normalizeTurnstileHost(rawHost string) string {
	trimmed := strings.TrimSpace(strings.ToLower(rawHost))
	if trimmed == "" {
		return ""
	}

	trimmed = strings.TrimSuffix(trimmed, ".")
	if trimmed == "" {
		return ""
	}

	host, _, err := net.SplitHostPort(trimmed)
	if err != nil {
		if strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "]") {
			withoutBrackets := strings.TrimPrefix(trimmed, "[")
			withoutBrackets = strings.TrimSuffix(withoutBrackets, "]")
			return strings.TrimSuffix(strings.ToLower(withoutBrackets), ".")
		}

		return trimmed
	}

	return strings.TrimSuffix(strings.ToLower(host), ".")
}
