package files

import "fmt"

// FileToolNames enumerates the tool identifiers that handle file content.
var FileToolNames = map[string]struct{}{
	"file_stat":   {},
	"file_read":   {},
	"file_write":  {},
	"file_delete": {},
	"file_list":   {},
	"file_search": {},
}

// RedactToolArguments removes sensitive payloads from tool arguments.
func RedactToolArguments(toolName string, args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	if _, ok := FileToolNames[toolName]; !ok {
		return args
	}
	cloned := cloneMap(args)
	if value, ok := cloned["content"]; ok {
		cloned["content"] = summarizeRedaction(value)
	}
	return cloned
}

// RedactToolResult removes sensitive payloads from tool results.
func RedactToolResult(toolName string, result map[string]any) map[string]any {
	if result == nil {
		return nil
	}
	if _, ok := FileToolNames[toolName]; !ok {
		return result
	}
	cloned := cloneMap(result)
	if value, ok := cloned["content"]; ok {
		cloned["content"] = summarizeRedaction(value)
	}
	if chunks, ok := cloned["chunks"]; ok {
		sanitized := redactChunks(chunks)
		if sanitized != nil {
			cloned["chunks"] = sanitized
		}
	}
	return cloned
}

// redactChunks removes chunk content from search results.
func redactChunks(value any) any {
	slice, ok := value.([]any)
	if !ok {
		return value
	}
	result := make([]any, 0, len(slice))
	for _, item := range slice {
		entry, ok := item.(map[string]any)
		if !ok {
			result = append(result, item)
			continue
		}
		cloned := cloneMap(entry)
		if content, ok := cloned["chunk_content"]; ok {
			cloned["chunk_content"] = summarizeRedaction(content)
		}
		result = append(result, cloned)
	}
	return result
}

// summarizeRedaction builds a lightweight redaction summary for logs.
func summarizeRedaction(value any) map[string]any {
	length := 0
	switch v := value.(type) {
	case string:
		length = len(v)
	case []byte:
		length = len(v)
	default:
		length = 0
	}
	return map[string]any{
		"redacted": true,
		"bytes":    length,
		"preview":  fmt.Sprintf("<redacted:%d>", length),
	}
}

// cloneMap performs a shallow copy of a map for redaction.
func cloneMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
