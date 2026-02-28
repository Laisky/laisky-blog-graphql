package mcp

import (
	"net/http"
	"strings"

	logSDK "github.com/Laisky/go-utils/v6/log"

	"github.com/Laisky/laisky-blog-graphql/library"
)

// withAuthorizationHeaderNormalization normalizes backward-compatible query
// API key authentication into the Authorization header so downstream code can
// consistently rely on a single auth channel.
//
// Parameters:
//   - next: downstream HTTP handler.
//   - logger: optional logger for debug diagnostics.
//
// Returns:
//   - wrapped HTTP handler that injects Authorization when possible.
func withAuthorizationHeaderNormalization(next http.Handler, logger logSDK.Logger) http.Handler {
	if next == nil {
		return nil
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader, source := resolveRequestAuthorizationHeader(r)
		if source == "query_apikey" && strings.TrimSpace(r.Header.Get("Authorization")) == "" {
			r.Header.Set("Authorization", authHeader)
			if logger != nil {
				logger.Debug("normalized legacy mcp query auth into authorization header; prefer Authorization header")
			}
		}

		next.ServeHTTP(w, r)
	})
}

// resolveRequestAuthorizationHeader resolves the canonical Authorization header
// value for an MCP HTTP request.
//
// Parameters:
//   - r: incoming HTTP request.
//
// Returns:
//   - authHeader: normalized authorization header value, or empty string when unavailable.
//   - source: where authorization was sourced from: "header", "query_apikey", or "none".
func resolveRequestAuthorizationHeader(r *http.Request) (authHeader string, source string) {
	if r == nil {
		return "", "none"
	}

	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header != "" {
		return header, "header"
	}

	apiKey := extractAPIKeyFromQuery(r)
	if apiKey != "" {
		return "Bearer " + apiKey, "query_apikey"
	}

	return "", "none"
}

// extractAPIKeyFromQuery extracts a backward-compatible API key from common
// MCP query parameters.
//
// Parameters:
//   - r: incoming HTTP request.
//
// Returns:
//   - apiKey: non-empty API key when present in query parameters; otherwise empty.
func extractAPIKeyFromQuery(r *http.Request) (apiKey string) {
	if r == nil || r.URL == nil {
		return ""
	}

	query := r.URL.Query()
	for _, key := range []string{"APIKEY", "apikey", "api_key"} {
		raw := strings.TrimSpace(query.Get(key))
		if raw == "" {
			continue
		}

		trimmed := strings.TrimSpace(library.StripBearerPrefix(raw))
		if trimmed != "" {
			return trimmed
		}
	}

	return ""
}
