package clientip

import (
	"net"
	"net/http"
	"strings"
)

// NormalizeHost returns a host in the form request hosts and configured site
// hosts are compared in: trimmed and lower-cased, without a port, IPv6
// brackets or a trailing dot. It returns "" for a blank host. It is the one
// hostname rule shared by site selection and Turnstile pair selection.
func NormalizeHost(value string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(strings.ToLower(value)), ".")
	if trimmed == "" {
		return ""
	}

	if host, _, err := net.SplitHostPort(trimmed); err == nil {
		return strings.TrimSuffix(host, ".")
	}
	if strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "]") {
		return strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]"), ".")
	}
	return trimmed
}

// NormalizeHosts normalizes each host with NormalizeHost and drops blank ones.
func NormalizeHosts(hosts []string) []string {
	result := make([]string, 0, len(hosts))
	for _, host := range hosts {
		if normalized := NormalizeHost(host); normalized != "" {
			result = append(result, normalized)
		}
	}
	return result
}

// RequestHostname returns the normalized host the client asked for (see
// RequestHost and NormalizeHost).
func RequestHostname(r *http.Request) string {
	return NormalizeHost(RequestHost(r))
}
