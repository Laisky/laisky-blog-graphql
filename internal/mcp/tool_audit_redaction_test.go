package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestToolAuditParametersDoNotLeakOrMutate verifies the policy wired before the call-log sink.
func TestToolAuditParametersDoNotLeakOrMutate(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
	}{
		{"web_fetch", map[string]any{"url": "https://example.com/synthetic-path-token?key=synthetic-query"}},
		{"extract_key_info", map[string]any{"query": "find", "materials": "synthetic-material"}},
		{"memory_after_turn", map[string]any{"current_input_text": "synthetic-memory"}},
		{"mcp_pipe", map[string]any{"steps": []any{
			map[string]any{"tool": "file_write", "args": map[string]any{"content": "synthetic-file"}},
			map[string]any{"tool": "web_fetch", "args": map[string]any{"url": "https://example.com/synthetic-path-token"}},
			map[string]any{"tool": "memory_after_turn", "args": map[string]any{"input_items": []any{map[string]any{"text": "synthetic-memory"}}}},
		}}},
	}
	for _, tc := range cases {
		before, err := json.Marshal(tc.args)
		if err != nil {
			t.Fatal(err)
		}
		logged, err := json.Marshal(redactToolAuditParameters(tc.name, tc.args))
		if err != nil || strings.Contains(string(logged), "synthetic-") {
			t.Fatalf("unsafe audit parameters for %s: %s (%v)", tc.name, logged, err)
		}
		after, err := json.Marshal(tc.args)
		if err != nil || string(after) != string(before) {
			t.Fatalf("redaction changed actual operation arguments for %s", tc.name)
		}
	}
}
