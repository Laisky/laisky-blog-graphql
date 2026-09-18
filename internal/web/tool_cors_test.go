package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestToolCORSExplicitConditionalHeaders(t *testing.T) {
	header := http.Header{}
	setToolCORSHeaders(header)
	for _, name := range []string{"Authorization", "If-Match", "If-None-Match", "Mcp-Session-Id", "MCP-Protocol-Version"} {
		if !strings.Contains(header.Get("Access-Control-Allow-Headers"), name) {
			t.Errorf("missing request header %s", name)
		}
	}
	for _, name := range []string{"ETag", "Mcp-Session-Id"} {
		if !strings.Contains(header.Get("Access-Control-Expose-Headers"), name) {
			t.Errorf("missing response header %s", name)
		}
	}
	if header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("header helper must not authorize an origin")
	}
}
