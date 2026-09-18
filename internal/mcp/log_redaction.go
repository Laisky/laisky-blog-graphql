package mcp

import (
	"encoding/json"

	"github.com/Laisky/laisky-blog-graphql/internal/library/toolpolicy"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpmemory "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory"
)

// redactedContentKey is the field name the shared file redactors key on. It is
// declared once so every redaction site uses the same field.
const redactedContentKey = "content"

// redactMCPBody redacts sensitive file content fields from MCP payloads.
func redactMCPBody(raw string) string {
	if raw == "" {
		return raw
	}
	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return `{"redacted":true,"reason":"invalid JSON payload"}`
	}
	redacted := redactMCPValue(payload)
	out, err := json.Marshal(redacted)
	if err != nil {
		return `{"redacted":true,"reason":"unencodable log payload"}`
	}
	return string(out)
}

// redactMCPValue recursively redacts nested payloads.
func redactMCPValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return redactMCPMap(v)
	case []any:
		result := make([]any, 0, len(v))
		for _, item := range v {
			result = append(result, redactMCPValue(item))
		}
		return result
	default:
		return value
	}
}

// redactMCPMap applies file-specific redaction to a JSON object.
func redactMCPMap(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = redactMCPValue(value)
	}

	method, _ := output["method"].(string)
	if method == "call_tool" || method == "tools/call" {
		params, _ := output["params"].(map[string]any)
		nameKey := "name"
		if method == "call_tool" {
			nameKey = "tool_name"
		}
		name, _ := params[nameKey].(string)
		if args, ok := params["arguments"].(map[string]any); ok {
			params["arguments"] = redactNamedToolArguments(name, args)
		}
	}
	// Pipeline steps use tool/args instead of method/params. Nested pipes have
	// already been visited recursively, and require the same content policy.
	if name, ok := output["tool"].(string); ok {
		if args, ok := output["args"].(map[string]any); ok {
			output["args"] = redactNamedToolArguments(name, args)
		}
	}
	if value, ok := output["url"]; ok {
		text, _ := value.(string)
		output["url"] = toolpolicy.URLForLog(text)
	}

	if _, ok := output[redactedContentKey]; ok {
		output[redactedContentKey] = files.RedactToolArguments("file_read", map[string]any{redactedContentKey: output[redactedContentKey]})[redactedContentKey]
	}
	if _, ok := output["chunk_content"]; ok {
		output["chunk_content"] = files.RedactToolArguments("file_search", map[string]any{redactedContentKey: output["chunk_content"]})[redactedContentKey]
	}
	// file_summary is response metadata that must never reach logs or audits (§7.2).
	if _, ok := output["file_summary"]; ok {
		output["file_summary"] = files.RedactToolArguments("file_search", map[string]any{redactedContentKey: output["file_summary"]})[redactedContentKey]
	}
	return output
}

// redactHookPayload renders a redacted JSON string for hook logging.
func redactHookPayload(payload any) string {
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return redactMCPBody(string(data))
}

// redactNamedToolArguments applies content policy to either transport's tool-shaped object.
func redactNamedToolArguments(name string, args map[string]any) map[string]any {
	redacted := files.RedactToolArguments(name, args)
	redacted = mcpmemory.RedactToolArguments(name, redacted)
	if name == "extract_key_info" {
		cloned := make(map[string]any, len(redacted))
		for key, value := range redacted {
			cloned[key] = value
		}
		if value, ok := cloned["materials"]; ok {
			cloned["materials"] = files.RedactToolArguments("file_write", map[string]any{redactedContentKey: value})[redactedContentKey]
		}
		return cloned
	}
	return redacted
}

// redactToolAuditParameters reuses recursive request redaction before persistent call logging.
// Only the logging copy is transformed; real operation arguments remain unchanged.
func redactToolAuditParameters(name string, args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	return redactMCPMap(redactNamedToolArguments(name, args))
}
