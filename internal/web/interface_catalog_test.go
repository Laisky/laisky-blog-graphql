package web

import (
	"reflect"
	"strings"
	"testing"
)

// TestInterfaceCatalogNeverLetsMCPOwnGraphQLOrHTTP exercises all independent availability combinations.
func TestInterfaceCatalogNeverLetsMCPOwnGraphQLOrHTTP(t *testing.T) {
	for _, registered := range []bool{false, true} {
		for _, backend := range []bool{false, true} {
			source := interfaceSources{Search: backend, Fetch: backend, Extraction: backend, Files: backend, FileWriter: backend,
				AskUser: backend, UserRequests: backend, CallLogs: backend}
			if registered {
				source.MCP = []string{"web_search", "web_fetch", "extract_key_info", "ask_user", "get_user_request"}
			}
			catalog := buildInterfaceCatalog(source)
			mcp, console := mcpToolGroups(catalog), consoleGroups(catalog)
			for _, name := range []string{"web_search", "web_fetch", "extract_key_info", "ask_user", "get_user_request"} {
				if mcp[name] != registered || console[name] != backend {
					t.Fatalf("%s: registration=%v backend=%v mcp=%v console=%v", name, registered, backend, mcp[name], console[name])
				}
			}
			for name, enabled := range catalog.HTTP {
				if enabled != backend {
					t.Fatalf("HTTP %s depends on MCP registration", name)
				}
			}
		}
	}
}

// TestInterfaceCatalogReportsHistoryAndWriterSeparately avoids overstating partial deployments.
func TestInterfaceCatalogReportsHistoryAndWriterSeparately(t *testing.T) {
	catalog := buildInterfaceCatalog(interfaceSources{Files: true, MCP: []string{"file_read"}})
	if !catalog.HTTP["GET /tools/file_io/api/versions"] || catalog.HTTP["PUT /tools/file_io/api/file"] {
		t.Fatal("history-only storage was incorrectly advertised as a version-aware writer")
	}
	if catalog.MCP["file_restore_version"] || mcpToolGroups(catalog)["file_io"] {
		t.Fatal("unregistered or incomplete file tool set was advertised")
	}
	if !strings.Contains(catalog.Scope, "not authorization") {
		t.Fatal("configuration scope is ambiguous")
	}
	if _, exists := catalog.GraphQL["FileRead"]; exists {
		t.Fatal("invented an unimplemented GraphQL field")
	}
}

// TestInterfaceCatalogDoesNotMutateItsSources keeps transport decisions independent and repeatable.
func TestInterfaceCatalogDoesNotMutateItsSources(t *testing.T) {
	input := []string{"file_read", "extension_tool"}
	catalog := buildInterfaceCatalog(interfaceSources{MCP: input})
	groups := consoleGroups(catalog)
	groups["web_search"] = true
	if catalog.MCP["web_search"] || !catalog.MCP["extension_tool"] || !reflect.DeepEqual(input, []string{"file_read", "extension_tool"}) {
		t.Fatal("availability generation mutated another interface")
	}
}
