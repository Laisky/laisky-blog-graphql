package controller

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	gconfig "github.com/Laisky/go-config/v2"
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

// validateTurnstileTokenForLogin validates Turnstile token before allowing SSO login.
// Parameters: ctx carries request context and metadata, turnstileToken is the optional token from the client.
// Returns: Nil when verification is disabled or succeeds; otherwise returns a wrapped error.
func validateTurnstileTokenForLogin(ctx context.Context, turnstileToken *string) error {
	secret := resolveTurnstileSecretForLogin(resolveTurnstileRequestHost(ctx))
	if secret == "" {
		return nil
	}

	token := ""
	if turnstileToken != nil {
		token = strings.TrimSpace(*turnstileToken)
	}

	if err := validateInputLength(turnstileTokenLengthLimit, token); err != nil {
		return errors.Wrap(err, "validate turnstile token length")
	}

	if token == "" {
		return errors.New("turnstile token is required")
	}

	if err := verifyTurnstileToken(ctx, secret, token, resolveTurnstileClientIP(ctx)); err != nil {
		return errors.Wrap(err, "verify turnstile token")
	}

	return nil
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

// resolveTurnstileRequestHost extracts normalized request host from context.
// Parameters: ctx carries optional Gin request context.
// Returns: A normalized host string, or empty string when unavailable.
func resolveTurnstileRequestHost(ctx context.Context) string {
	gctx, ok := gmw.GetGinCtxFromStdCtx(ctx)
	if !ok || gctx == nil || gctx.Request == nil {
		return ""
	}

	forwarded := strings.TrimSpace(gctx.Request.Header.Get("X-Forwarded-Host"))
	if forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if len(parts) > 0 {
			return normalizeTurnstileHost(parts[0])
		}
	}

	return normalizeTurnstileHost(gctx.Request.Host)
}

// resolveTurnstileClientIP extracts a validated client IP from context.
// Parameters: ctx carries optional Gin request context.
// Returns: A canonical client IP string, or empty string when unavailable.
func resolveTurnstileClientIP(ctx context.Context) string {
	gctx, ok := gmw.GetGinCtxFromStdCtx(ctx)
	if !ok || gctx == nil {
		return ""
	}

	ip := strings.TrimSpace(gctx.ClientIP())
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ""
	}

	return parsed.String()
}

// resolveTurnstileSecretForLogin selects the turnstile secret key for the current request host.
// Parameters: requestHost is the normalized request host used to match site settings.
// Returns: The matched secret key when configured; otherwise an empty string.
func resolveTurnstileSecretForLogin(requestHost string) string {
	globalSecret := strings.TrimSpace(gconfig.Shared.GetString("settings.web.turnstile.secret_key"))
	rawSites := gconfig.Shared.GetStringMap("settings.web.sites")
	if len(rawSites) == 0 {
		return globalSecret
	}

	keys := make([]string, 0, len(rawSites))
	for key := range rawSites {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	defaultSecret := ""
	for _, key := range keys {
		base := "settings.web.sites." + key
		secret := strings.TrimSpace(gconfig.Shared.GetString(base + ".turnstile_secret_key"))
		if secret == "" {
			continue
		}

		if defaultSecret == "" && gconfig.Shared.GetBool(base+".default") {
			defaultSecret = secret
		}

		if requestHost != "" && isTurnstileHostMatched(base, requestHost) {
			return secret
		}
	}

	if defaultSecret != "" {
		return defaultSecret
	}

	return globalSecret
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
	if err == nil {
		return strings.TrimSuffix(strings.ToLower(host), ".")
	}

	if strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "]") {
		withoutBrackets := strings.TrimPrefix(trimmed, "[")
		withoutBrackets = strings.TrimSuffix(withoutBrackets, "]")
		return strings.TrimSuffix(strings.ToLower(withoutBrackets), ".")
	}

	return trimmed
}
