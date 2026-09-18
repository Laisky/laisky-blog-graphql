package web

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// modernPreflightBudget creates the browser's complete CORS-unsafe name list.
func modernPreflightBudget(t *testing.T, count, bytes int) (string, []string) {
	t.Helper()
	fixed := []string{"authorization", "content-type", "mcp-method", "mcp-name", "mcp-protocol-version"}
	mirrored := make([]string, count)
	for i := range mirrored {
		mirrored[i] = fmt.Sprintf("mcp-param-h%d", i)
	}
	if bytes > 0 {
		padding := bytes - len(strings.Join(append(append([]string{}, fixed...), mirrored...), ","))
		if padding < 0 || len(mirrored) == 0 {
			t.Fatal("invalid test fixture budget")
		}
		mirrored[len(mirrored)-1] += strings.Repeat("x", padding)
	}
	all := append(append([]string{}, fixed...), mirrored...)
	sort.Strings(all)
	requested := strings.Join(all, ",")
	if bytes > 0 && len(requested) != bytes {
		t.Fatalf("fixture has %d bytes, expected %d", len(requested), bytes)
	}
	return requested, mirrored
}

// TestToolCORSMirroredBudget pins independent server boundaries for client parity.
func TestToolCORSMirroredBudget(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		count, bytes int
		allowed      bool
	}{
		{"100 names", 100, 0, true}, {"101 names", 101, 0, false},
		{"one name 8192 bytes", 1, 8192, true}, {"one name 8193 bytes", 1, 8193, false},
		{"50 names 8192 bytes", 50, 8192, true}, {"50 names 8193 bytes", 50, 8193, false},
		{"100 names 8192 bytes", 100, 8192, true}, {"100 names 8193 bytes", 100, 8193, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requested, names := modernPreflightBudget(t, tc.count, tc.bytes)
			header := http.Header{}
			setToolCORSHeaders(header, requested)
			allowed := map[string]bool{}
			for _, name := range strings.Split(header.Get("Access-Control-Allow-Headers"), ",") {
				allowed[strings.ToLower(strings.TrimSpace(name))] = true
			}
			for _, name := range names {
				if allowed[name] != tc.allowed {
					t.Fatalf("dynamic admission=%v, expected=%v; count=%d bytes=%d", allowed[name], tc.allowed, tc.count, len(requested))
				}
			}
			if !allowed["authorization"] || !allowed["content-type"] {
				t.Fatal("fixed headers were dropped")
			}
		})
	}
}
