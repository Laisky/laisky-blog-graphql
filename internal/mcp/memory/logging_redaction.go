package memory

import "fmt"

// ToolNames enumerates memory tool identifiers with potentially sensitive payloads.
var ToolNames = map[string]struct{}{
	"memory_before_turn":            {},
	"memory_after_turn":             {},
	"memory_run_maintenance":        {},
	"memory_list_dir_with_abstract": {},
}

// RedactToolArguments removes sensitive content fields from memory tool arguments.
func RedactToolArguments(toolName string, args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	if _, ok := ToolNames[toolName]; !ok {
		return args
	}

	cloned := cloneMap(args)
	if value, ok := cloned["current_input"]; ok {
		cloned["current_input"] = summarizeRedaction(value)
	}
	if value, ok := cloned["input_items"]; ok {
		cloned["input_items"] = summarizeRedaction(value)
	}
	if value, ok := cloned["output_items"]; ok {
		cloned["output_items"] = summarizeRedaction(value)
	}
	return cloned
}

// summarizeRedaction builds a compact redaction marker.
func summarizeRedaction(value any) map[string]any {
	preview := fmt.Sprintf("<redacted:%T>", value)
	return map[string]any{
		"redacted": true,
		"preview":  preview,
	}
}

// cloneMap shallow-copies a map for safe redaction.
func cloneMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
