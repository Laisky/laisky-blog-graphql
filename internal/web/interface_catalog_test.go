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
	// FileIO is now published on GraphQL as a peer interface, so the field must
	// exist. Its availability still follows the backend it needs, not MCP tool
	// registration: history-only storage without a version-aware writer cannot
	// serve a read or a mutation, but can serve history metadata.
	writerBacked, exists := catalog.GraphQL["FileRead"]
	if !exists {
		t.Fatal("GraphQL FileIO field inventory is missing FileRead")
	}
	if writerBacked {
		t.Fatal("a read was advertised without a version-aware writer backend")
	}
	if !catalog.GraphQL["FileListVersions"] || !catalog.GraphQL["FileReadVersion"] {
		t.Fatal("history-backed GraphQL fields were hidden even though storage is configured")
	}
	if catalog.GraphQL["FileWrite"] || catalog.GraphQL["FileRestoreVersion"] {
		t.Fatal("a mutation was advertised without a version-aware writer backend")
	}
	// Memory has its own backend; storage alone must not advertise it.
	for _, field := range []string{"MemoryBeforeTurn", "MemoryAfterTurn",
		"MemoryRunMaintenance", "MemoryListDirWithAbstract"} {
		if catalog.GraphQL[field] {
			t.Fatalf("%s was advertised without a memory service", field)
		}
	}
}

// TestGraphQLFileIOAvailabilityIsIndependentOfMCPRegistration keeps the peer
// relationship honest: switching every MCP file tool off must not remove the
// GraphQL fields, and registering them must not add a GraphQL field that has
// no backend.
func TestGraphQLFileIOAvailabilityIsIndependentOfMCPRegistration(t *testing.T) {
	withoutMCP := buildInterfaceCatalog(interfaceSources{Files: true, FileWriter: true, Memory: true})
	for _, field := range []string{"FileStat", "FileRead", "FileList", "FileSearch",
		"FileWrite", "FileDelete", "FileRename", "FileRestoreVersion",
		"MemoryBeforeTurn", "MemoryListDirWithAbstract"} {
		if !withoutMCP.GraphQL[field] {
			t.Fatalf("%s disappeared because no MCP tool was registered", field)
		}
	}
	if mcpToolGroups(withoutMCP)["file_io"] || mcpToolGroups(withoutMCP)["memory"] {
		t.Fatal("unregistered MCP tools were advertised as available")
	}
	// The console follows whichever transport can actually serve the page.
	if !consoleGroups(withoutMCP)["file_io"] || !consoleGroups(withoutMCP)["memory"] {
		t.Fatal("a page backed only by GraphQL was hidden")
	}

	withMCPOnly := buildInterfaceCatalog(interfaceSources{MCP: []string{
		"file_stat", "file_read", "file_write", "file_delete", "file_rename", "file_list", "file_search",
	}})
	for _, field := range []string{"FileStat", "FileWrite"} {
		if withMCPOnly.GraphQL[field] {
			t.Fatalf("%s was advertised with no backend just because an MCP tool exists", field)
		}
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
