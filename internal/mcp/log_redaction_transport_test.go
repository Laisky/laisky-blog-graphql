package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMCPRedactionTransportGenerations protects real tools/call and nested pipeline inputs.
func TestMCPRedactionTransportGenerations(t *testing.T) {
	for _, raw := range []string{
		`{"method":"tools/call","params":{"name":"web_fetch","arguments":{"url":"https://example.com/reset/synthetic-path-token?key=synthetic-query#synthetic-fragment"}}}`,
		`{"method":"tools/call","params":{"name":"extract_key_info","arguments":{"query":"find","materials":"synthetic-private-material"}}}`,
		`{"method":"call_tool","params":{"tool_name":"web_fetch","arguments":{"url":"https://user:synthetic-password@example.com/synthetic-path-token"}}}`,
		`{"method":"tools/call","params":{"name":"memory_after_turn","arguments":{"current_input_text":"synthetic-memory-text","input_items":[{"text":"synthetic-message"}]}}}`,
		`{"method":"tools/call","params":{"name":"mcp_pipe","arguments":{"steps":[{"tool":"memory_after_turn","args":{"current_input_text":"synthetic-memory-text","output_items":[{"text":"s` +
			`ynthetic-output"}]}},{"tool":"web_fetch","args":{"url":"https://example.com/synthetic-path-token"}}]}}}`,
	} {
		redacted := redactMCPBody(raw)
		if strings.Contains(redacted, "synthetic-") {
			t.Errorf("sensitive request bytes survived the log redactor: %s", redacted)
		}
		if !json.Valid([]byte(redacted)) || !strings.Contains(redacted, `"method"`) {
			t.Errorf("redaction discarded the valid transport metadata: %s", redacted)
		}
	}
}

// TestMCPRedactionMalformedBodyFailsClosed covers prefixes captured before a JSON body is complete.
func TestMCPRedactionMalformedBodyFailsClosed(t *testing.T) {
	for _, raw := range []string{
		`{"method":"tools/call","params":{"arguments":{"content":"synthetic-partial-file`,
		`{"url":"https://example.com/synthetic-path-token?key=synthetic-query`,
		"synthetic-invalid-request\xff",
	} {
		redacted := redactMCPBody(raw)
		if strings.Contains(redacted, "synthetic-") || !json.Valid([]byte(redacted)) {
			t.Fatalf("malformed body was logged verbatim: %s", redacted)
		}
	}
}

// TestMCPRedactionPreservesMetadata confirms privacy does not simply remove all diagnostics.
func TestMCPRedactionPreservesMetadata(t *testing.T) {
	raw := `{"jsonrpc":"2.0","id":"r-1","method":"tools/call","params":{"name":"file_write","arguments":{"project":"notes","path":"/a.txt","content":"synthetic-file"}}}`
	redacted := redactMCPBody(raw)
	for _, retained := range []string{`"r-1"`, `"tools/call"`, `"file_write"`, `"notes"`, `"/a.txt"`} {
		if !strings.Contains(redacted, retained) {
			t.Errorf("lost non-content diagnostic %s", retained)
		}
	}
	if strings.Contains(redacted, "synthetic-file") {
		t.Fatal("file content was not redacted")
	}
}
