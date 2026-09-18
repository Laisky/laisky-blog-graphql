package web

import (
	"net/http"
	"sort"
	"strings"
)

const toolCORSRequestHeaders = "Authorization, Content-Type, Accept, If-Match, If-None-Match, Mcp-Session-Id, MCP-Protocol-Version, Mcp-Method, Mcp-Name, Last-Event-ID, Cache-Control, Pragma"
const toolCORSResponseHeaders = "ETag, Mcp-Session-Id, MCP-Protocol-Version, Retry-After"

// setToolCORSHeaders runs after origin validation. Only protocol-defined mirrored
// parameter headers may extend the fixed list; this is not arbitrary reflection.
func setToolCORSHeaders(header http.Header, requested ...string) {
	allowed := toolCORSRequestHeaders
	if len(requested) > 0 && len(requested[0]) <= 8192 {
		seen := map[string]bool{}
		var mirrored []string
		for _, raw := range strings.Split(requested[0], ",") {
			name := strings.TrimSpace(raw)
			lower := strings.ToLower(name)
			if !strings.HasPrefix(lower, "mcp-param-") || len(name) == len("mcp-param-") || seen[lower] || !httpHeaderToken(name) {
				continue
			}
			seen[lower] = true
			mirrored = append(mirrored, name)
		}
		sort.Strings(mirrored)
		if len(mirrored) > 0 && len(mirrored) <= 100 {
			allowed += ", " + strings.Join(mirrored, ", ")
			header.Add("Vary", "Access-Control-Request-Headers")
		}
	}
	header.Set("Access-Control-Allow-Headers", allowed)
	header.Set("Access-Control-Expose-Headers", toolCORSResponseHeaders)
}

// httpHeaderToken accepts RFC field-name bytes, excluding whitespace and controls.
func httpHeaderToken(value string) bool {
	if value == "" {
		return false
	}
	for _, ch := range value {
		if ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || strings.ContainsRune("!#$%&'*+-.^_`|~", ch) {
			continue
		}
		return false
	}
	return true
}
