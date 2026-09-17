package toolpolicy

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"strings"
	"testing"
)

// TestURLForLogOriginOnly reproduces PR #49's path-token disclosure. All
// credentials below are synthetic. Assert the whole result, not a blacklist
// that could miss encoded or unfamiliar secret formats.
func TestURLForLogOriginOnly(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, raw, want string }{
		{"plain path token", "https://example.com/reset/synthetic-path-token", "https://example.com"},
		{"escaped path token", "https://example.com/reset/%73ynthetic%2Dpath%2Ftoken", "https://example.com"},
		{"double escaped path", "https://example.com/reset/%2573ynthetic%252Ftoken", "https://example.com"},
		{"path parameter", "https://example.com/a;session=synthetic-token/b", "https://example.com"},
		{"unicode path", "https://example.com/用户/凭据", "https://example.com"},
		{"all sensitive components", "https://user:synthetic-password@example.com:8443/reset/synthetic-path?token=synthetic-query#synthetic-fragment", "https://example.com:8443"},
		{"ipv6 authority", "https://[2606:4700:4700::1111]:8443/synthetic-path?secret=value", "https://[2606:4700:4700::1111]:8443"},
		{"plain http", "http://example.com:8080/synthetic-path", "http://example.com:8080"},
		{"root path", "https://example.com/", "https://example.com"},
		{"bare origin", "https://example.com", "https://example.com"},
		{"empty query marker", "https://example.com/?", "https://example.com"},
		{"whitespace and case", "  HTTPS://Example.COM/synthetic-path  ", "https://Example.COM"},
		{"malformed path escape", "https://example.com/%zzsynthetic-token", "[invalid URL]"},
		{"malformed authority", "https://synthetic-token@bad%host/path", "[invalid URL]"},
		{"control character", "https://example.com/\nsynthetic-token", "[invalid URL]"},
		{"relative path", "/synthetic-token", "[invalid URL]"},
		{"schemeless authority", "//example.com/synthetic-token", "[invalid URL]"},
		{"opaque url", "https:synthetic-token", "[invalid URL]"},
		{"unsupported scheme", "ftp://example.com/synthetic-token", "[invalid URL]"},
		{"missing host", "http:///synthetic-token", "[invalid URL]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := URLForLog(tc.raw)
			if got != tc.want {
				t.Fatalf("URLForLog() = %q, want %q", got, tc.want)
			}
			if again := URLForLog(got); again != got {
				t.Fatalf("redaction is not idempotent: %q -> %q", got, again)
			}
		})
	}
}

// TestURLForLogAllowedRequestAndAuditField excludes the false-positive case
// where the only leaking fixture is already rejected by URL admission. It uses
// the real admission function with deterministic DNS and the real log sanitizer;
// the JSON encoding is a field-level check, not a database or logger integration.
func TestURLForLogAllowedRequestAndAuditField(t *testing.T) {
	t.Parallel()
	const raw = "https://example.com/reset/synthetic-path-token?token=synthetic-query#synthetic-fragment"
	lookup := func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host != "example.com" {
			t.Fatalf("unexpected lookup: %q", host)
		}
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	if err := validateFetchURL(context.Background(), raw, lookup); err != nil {
		t.Fatalf("fixture must reach the fetch/logging path: %v", err)
	}
	params := map[string]any{"url": URLForLog(raw), "output_markdown": true}
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "synthetic-") {
		t.Fatalf("serialized URL field discloses a synthetic token: %s", encoded)
	}
	if params["url"] != "https://example.com" {
		t.Fatalf("unexpected audit URL: %v", params["url"])
	}
	// Logging redaction is not URL rewriting: callers still fetch the full target.
	parsed, err := url.Parse(raw)
	if err != nil || parsed.RequestURI() != "/reset/synthetic-path-token?token=synthetic-query" {
		t.Fatalf("request target changed: %v, %v", parsed, err)
	}
}

// FuzzURLForLogOriginOnly enforces the redaction invariant for arbitrary input
// without DNS, network, credentials, or database dependencies. Seed cases also
// run as ordinary regression tests without enabling a fuzzing or benchmark job.
func FuzzURLForLogOriginOnly(f *testing.F) {
	for _, seed := range []string{
		"https://example.com/reset/synthetic-token",
		"https://example.com/%73ynthetic%2Ftoken",
		"https://user:password@example.com:8443/path?token=value#fragment",
		"https://[2606:4700:4700::1111]:8443/path",
		"https://example.com/?", "http:opaque", "//relative/path", "https://bad%host/secret",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		got := URLForLog(raw)
		if got == "[invalid URL]" {
			return
		}
		parsed, err := url.Parse(got)
		if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			t.Fatalf("invalid redacted origin %q: %v", got, err)
		}
		if parsed.User != nil || parsed.Opaque != "" || parsed.Path != "" || parsed.RawPath != "" ||
			parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
			t.Fatalf("redacted URL retains non-origin components: %q", got)
		}
		if again := URLForLog(got); again != got {
			t.Fatalf("redaction is not idempotent: %q -> %q", got, again)
		}
	})
}
