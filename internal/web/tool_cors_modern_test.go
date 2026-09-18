package web

import (
	"net/http"
	"strings"
	"testing"
)

// TestModernMCPHeadersAreAllowed checks the actual browser transport's required metadata headers.
func TestModernMCPHeadersAreAllowed(t *testing.T) {
	header := http.Header{}
	setToolCORSHeaders(header)
	allowed := strings.ToLower(header.Get("Access-Control-Allow-Headers"))
	for _, name := range []string{"authorization", "mcp-protocol-version", "mcp-method", "mcp-name", "if-match", "if-none-match"} {
		if !strings.Contains(allowed, name) {
			t.Fatalf("required browser header %s is missing", name)
		}
	}
}

// TestMirroredCORSHeadersStayWithinProtocolNamespace rejects arbitrary or malformed header reflection.
func TestMirroredCORSHeadersStayWithinProtocolNamespace(t *testing.T) {
	header := http.Header{}
	setToolCORSHeaders(header, "Mcp-Param-Tenant, mcp-param-tenant, Mcp-Param-Region, Cookie, X-Admin, Mcp-Param-, Mcp-Param-Injected\r\nX-Evil")
	allowed := header.Get("Access-Control-Allow-Headers")
	for _, name := range []string{"Mcp-Param-Tenant", "Mcp-Param-Region"} {
		if !strings.Contains(allowed, name) {
			t.Fatalf("missing mirrored header %s", name)
		}
	}
	for _, name := range []string{"Cookie", "X-Admin", "Injected", "X-Evil", "mcp-param-tenant"} {
		if strings.Contains(allowed, name) {
			t.Fatalf("reflected unwanted header %s", name)
		}
	}
	if header.Get("Vary") != "Access-Control-Request-Headers" {
		t.Fatal("dynamic preflight response is missing its Vary key")
	}
}
