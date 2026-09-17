package web

// consoleToolAvailability describes the interface used by each browser page.
// MCP tools and GraphQL fields are peers. Changing MCP registration cannot hide
// or disable a page that invokes an available GraphQL function directly.
// Maps are copied so the MCP catalog is never mutated as a side effect.
func consoleToolAvailability(mcpTools, graphqlTools map[string]bool) map[string]bool {
	console := make(map[string]bool, len(mcpTools))
	for name, enabled := range mcpTools {
		console[name] = enabled
	}
	for _, name := range []string{"web_search", "web_fetch", "extract_key_info"} {
		console[name] = graphqlTools[name]
	}
	return console
}
