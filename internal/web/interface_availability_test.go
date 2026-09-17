package web

import "testing"

func TestConsoleGraphQLAvailabilityIndependentOfMCP(t *testing.T) {
	for _, mcpEnabled := range []bool{false, true} {
		for _, graphqlEnabled := range []bool{false, true} {
			mcpTools := map[string]bool{"web_search": mcpEnabled, "web_fetch": mcpEnabled, "extract_key_info": mcpEnabled, "file_io": mcpEnabled}
			graphqlTools := map[string]bool{"web_search": graphqlEnabled, "web_fetch": graphqlEnabled, "extract_key_info": graphqlEnabled}
			console := consoleToolAvailability(mcpTools, graphqlTools)
			for name := range graphqlTools {
				if console[name] != graphqlEnabled {
					t.Fatalf("%s availability followed MCP=%v instead of GraphQL=%v", name, mcpEnabled, graphqlEnabled)
				}
				if mcpTools[name] != mcpEnabled {
					t.Fatalf("%s: browser availability mutated the MCP catalog", name)
				}
			}
			if console["file_io"] != mcpEnabled {
				t.Fatal("the FileIO browser still uses MCP for its file operations")
			}
		}
	}
}

func TestMissingGraphQLBackendDoesNotInheritMCPAvailability(t *testing.T) {
	console := consoleToolAvailability(map[string]bool{"web_fetch": true}, nil)
	if console["web_fetch"] {
		t.Fatal("a registered MCP tool cannot establish GraphQL backend availability")
	}
}
