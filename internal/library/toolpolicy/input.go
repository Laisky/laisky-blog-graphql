// Package toolpolicy holds transport-independent validation for shared tools.
package toolpolicy

import (
	"context"
	"encoding/json"
	"math"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxQueryBytes bounds shared search and extraction query input before billing.
const MaxQueryBytes = 16 << 10

// The only fetch schemes this policy admits. Declared once so admission, the
// log redactor and the egress policy cannot accept different sets.
const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"
)

// InputError describes invalid input without echoing credentials or request bodies.
type InputError struct{ Field, Reason string }

// Error implements the error interface without including the original input.
func (e *InputError) Error() string { return e.Field + " " + e.Reason }

// invalid builds an InputError without echoing the rejected input.
func invalid(field, reason string) error { return &InputError{Field: field, Reason: reason} }

// Query normalizes and validates a query identically for MCP and GraphQL callers.
func Query(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", invalid("query", "must be valid UTF-8")
	}
	query := strings.TrimSpace(raw)
	if query == "" {
		return "", invalid("query", "cannot be empty")
	}
	if len(query) > MaxQueryBytes {
		return "", invalid("query", "exceeds 16384 UTF-8 bytes")
	}
	return query, nil
}

// OptionalInt accepts only a JSON integer in range. Explicit null means omitted,
// matching a nullable GraphQL Int argument. No string coercion or truncation occurs.
func OptionalInt(args map[string]any, key string, fallback, minimum, maximum int) (int, error) {
	value, present := args[key]
	if !present || value == nil {
		return fallback, nil
	}
	var n float64
	switch v := value.(type) {
	case int:
		n = float64(v)
	case int32:
		n = float64(v)
	case int64:
		n = float64(v)
	case float64:
		n = v
	case float32:
		n = float64(v)
	case json.Number:
		var err error
		n, err = v.Float64()
		if err != nil {
			return 0, invalid(key, "must be an integer")
		}
	default:
		return 0, invalid(key, "must be an integer, not a string or boolean")
	}
	// GraphQL Int is signed 32-bit. Check before narrowing, including on 32-bit Go.
	if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < math.MinInt32 || n > math.MaxInt32 || n < float64(minimum) || n > float64(maximum) {
		return 0, invalid(key, "must be an integer in the advertised range")
	}
	return int(n), nil
}

// OptionalBool accepts only a JSON boolean; explicit null selects the default.
func OptionalBool(args map[string]any, key string, fallback bool) (bool, error) {
	value, present := args[key]
	if !present || value == nil {
		return fallback, nil
	}
	result, ok := value.(bool)
	if !ok {
		return false, invalid(key, "must be a boolean")
	}
	return result, nil
}

// ValidateFetchURL applies the shared request-admission policy before billing.
// The remote browser must independently enforce connection and redirect policy;
// DNS validation here alone cannot prevent rebinding at a separate crawler host.
func ValidateFetchURL(ctx context.Context, raw string) error {
	return validateFetchURL(ctx, raw, net.DefaultResolver.LookupIPAddr)
}

type ipLookup func(context.Context, string) ([]net.IPAddr, error)

// validateFetchURL is ValidateFetchURL with an injectable resolver, so a test
// can pin a decision to a known DNS answer.
func validateFetchURL(ctx context.Context, raw string, lookup ipLookup) error {
	if ctx == nil {
		return invalid("url", "context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(raw) > 8192 || !utf8.ValidString(raw) {
		return invalid("url", "must be valid UTF-8 within 8192 bytes")
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return invalid("url", "malformed URL")
	}
	if parsed.Scheme != schemeHTTP && parsed.Scheme != schemeHTTPS {
		return invalid("url", "only http and https are allowed")
	}
	if parsed.Opaque != "" || parsed.User != nil || parsed.Hostname() == "" || strings.Contains(parsed.Hostname(), "%") {
		return invalid("url", "requires a hostname without credentials or an address zone")
	}
	if port := parsed.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return invalid("url", "invalid port")
		}
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return invalid("url", "local hostnames are not allowed")
	}
	if literal, err := netip.ParseAddr(host); err == nil {
		if !publicAddress(literal) {
			return invalid("url", "non-public addresses are not allowed")
		}
		return nil
	}
	// Browsers accept legacy decimal/octal/hex IPv4 forms that net.ParseIP rejects.
	// Reject numeric-looking authorities instead of resolving one meaning and
	// allowing the renderer to connect to another.
	numeric := true
	for _, r := range host {
		if (r < '0' || r > '9') && r != '.' {
			numeric = false
			break
		}
	}
	if numeric || strings.HasPrefix(host, "0x") {
		return invalid("url", "non-canonical numeric host")
	}
	dnsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	addresses, err := lookup(dnsCtx, host)
	if err != nil {
		if dnsCtx.Err() != nil {
			return dnsCtx.Err()
		}
		return invalid("url", "hostname cannot be resolved")
	}
	if len(addresses) == 0 {
		return invalid("url", "hostname has no addresses")
	}
	for _, address := range addresses {
		ip, ok := netip.AddrFromSlice(address.IP)
		if !ok || address.Zone != "" || !publicAddress(ip) {
			return invalid("url", "hostname resolves to a non-public address")
		}
	}
	return nil
}

// publicAddress reports whether an address is globally routable. Loopback,
// private, link-local and the reserved/benchmark ranges are all rejected, so a
// renderer cannot be steered at infrastructure the caller does not own.
func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "240.0.0.0/4"} {
		if netip.MustParsePrefix(prefix).Contains(ip) {
			return false
		}
	}
	return true
}

// URLForLog returns only the HTTP(S) scheme and host (including any port).
// Paths, including RawPath, may contain bearer tokens and are never logged.
// Credentials, queries and fragments are also removed; malformed input is never
// returned verbatim. Use this only for log fields, not as the fetch target.
func URLForLog(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != schemeHTTP && parsed.Scheme != schemeHTTPS) {
		return "[invalid URL]"
	}
	parsed.User = nil
	parsed.Path, parsed.RawPath = "", ""
	parsed.RawQuery, parsed.Fragment, parsed.RawFragment = "", "", ""
	parsed.ForceQuery = false
	return parsed.String()
}
