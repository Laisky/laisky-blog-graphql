package mcp

import (
	"encoding/json"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// redactMCPBody redacts sensitive file content fields from MCP payloads.
func redactMCPBody(raw string) string {
	if raw == "" {
		return raw
	}
	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return raw
	}
	redacted := redactMCPValue(payload)
	out, err := json.Marshal(redacted)
	if err != nil {
		return raw
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
	if method == "call_tool" {
		params, _ := output["params"].(map[string]any)
		toolName, _ := params["tool_name"].(string)
		if _, ok := files.FileToolNames[toolName]; ok {
			if args, ok := params["arguments"].(map[string]any); ok {
				params["arguments"] = files.RedactToolArguments(toolName, args)
			}
		}
	}

	if _, ok := output["content"]; ok {
		output["content"] = files.RedactToolArguments("file_read", map[string]any{"content": output["content"]})["content"]
	}
	if _, ok := output["chunk_content"]; ok {
		output["chunk_content"] = files.RedactToolArguments("file_search", map[string]any{"content": output["chunk_content"]})["content"]
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
