package web

// Canonical shared-tool names. Declared once so the catalog, the availability
// projection and the pricing metadata cannot drift from each other.
const (
	toolNameWebSearch      = "web_search"
	toolNameWebFetch       = "web_fetch"
	toolNameExtractKeyInfo = "extract_key_info"
)

// runtimeConfigInterfacesKey is the runtime-config payload key that publishes
// the configured interface catalog to the browser.
const runtimeConfigInterfacesKey = "interfaces"

// interfaceSources records independently constructed services and the actual MCP registry.
// It deliberately contains no MCP registration flags for GraphQL or HTTP decisions.
type interfaceSources struct {
	MCP                                          []string
	Search, Fetch, Extraction, Files, FileWriter bool
	AskUser, UserRequests, CallLogs, Memory      bool
}

// interfaceCatalog distinguishes configuration from per-request authorization and health.
type interfaceCatalog struct {
	Scope   string          `json:"scope"`
	MCP     map[string]bool `json:"mcp"`
	GraphQL map[string]bool `json:"graphql"`
	HTTP    map[string]bool `json:"http"`
}

// buildInterfaceCatalog reports registered adapters, not promises inferred from static cards.
func buildInterfaceCatalog(source interfaceSources) interfaceCatalog {
	catalog := interfaceCatalog{
		Scope: "shared-tools; configured adapters only; not authorization or backend health",
		MCP:   make(map[string]bool),
		// GraphQL availability follows the backend each field needs, never MCP
		// tool registration. FileIO and memory are published here as peer
		// fields, so disabling an MCP tool cannot remove them from the schema.
		GraphQL: map[string]bool{
			"WebSearch": source.Search, "WebFetch": source.Fetch, "ExtractKeyInfo": source.Extraction,
			"FileStat": source.FileWriter, "FileRead": source.FileWriter, "FileList": source.FileWriter,
			"FileSearch": source.FileWriter, "FileWrite": source.FileWriter, "FileDelete": source.FileWriter,
			"FileRename": source.FileWriter, "FileRestoreVersion": source.FileWriter && source.Files,
			"FileListVersions": source.Files, "FileReadVersion": source.Files,
			"MemoryBeforeTurn": source.Memory, "MemoryAfterTurn": source.Memory,
			"MemoryRunMaintenance": source.Memory, "MemoryListDirWithAbstract": source.Memory,
		},
		HTTP: map[string]bool{
			"GET /tools/file_io/api/versions":               source.Files,
			"GET /tools/file_io/api/versions/{id}/content":  source.Files,
			"PUT /tools/file_io/api/file":                   source.Files && source.FileWriter,
			"POST /tools/file_io/api/versions/{id}/restore": source.Files && source.FileWriter,
			"/tools/ask_user/*":                             source.AskUser,
			"/tools/get_user_requests/api/*":                source.UserRequests,
			"/tools/call_log/api/*":                         source.CallLogs,
		},
	}
	for _, name := range []string{
		toolNameWebSearch, toolNameWebFetch, toolNameExtractKeyInfo, "ask_user", "get_user_request", "find_tool", "mcp_pipe",
		"file_stat", "file_read", "file_write", "file_delete", "file_rename", "file_list", "file_search",
		"file_list_versions", "file_read_version", "file_restore_version",
		"memory_before_turn", "memory_after_turn", "memory_run_maintenance", "memory_list_dir_with_abstract",
	} {
		catalog.MCP[name] = false
	}
	for _, name := range source.MCP {
		catalog.MCP[name] = true
	}
	return catalog
}

// mcpToolGroups preserves the existing browser metadata shape using actual registered names.
func mcpToolGroups(catalog interfaceCatalog) map[string]bool {
	all := func(names ...string) bool {
		for _, name := range names {
			if !catalog.MCP[name] {
				return false
			}
		}
		return true
	}
	return map[string]bool{
		"web_search": catalog.MCP["web_search"], "web_fetch": catalog.MCP["web_fetch"],
		toolNameExtractKeyInfo: catalog.MCP[toolNameExtractKeyInfo], "ask_user": catalog.MCP["ask_user"],
		"get_user_request": catalog.MCP["get_user_request"],
		"file_io":          all("file_stat", "file_read", "file_write", "file_delete", "file_rename", "file_list", "file_search"),
		"memory":           all("memory_before_turn", "memory_after_turn", "memory_run_maintenance", "memory_list_dir_with_abstract"),
	}
}

// graphQLToolGroups projects the typed GraphQL fields onto the browser's
// per-tool grouping, so a page can tell that a capability is reachable over
// GraphQL even when the corresponding MCP tool is switched off.
func graphQLToolGroups(catalog interfaceCatalog) map[string]bool {
	all := func(names ...string) bool {
		for _, name := range names {
			if !catalog.GraphQL[name] {
				return false
			}
		}
		return true
	}
	return map[string]bool{
		toolNameWebSearch:      catalog.GraphQL["WebSearch"],
		toolNameWebFetch:       catalog.GraphQL["WebFetch"],
		toolNameExtractKeyInfo: catalog.GraphQL["ExtractKeyInfo"],
		"file_io": all("FileStat", "FileRead", "FileList", "FileSearch",
			"FileWrite", "FileDelete", "FileRename"),
		"memory": all("MemoryBeforeTurn", "MemoryAfterTurn",
			"MemoryRunMaintenance", "MemoryListDirWithAbstract"),
	}
}

// consoleGroups follows each page's actual transport, never a supposed preferred entrypoint.
func consoleGroups(catalog interfaceCatalog) map[string]bool {
	graphql := graphQLToolGroups(catalog)
	groups := consoleToolAvailability(mcpToolGroups(catalog), graphql)
	groups["ask_user"] = catalog.HTTP["/tools/ask_user/*"]
	groups["get_user_request"] = catalog.HTTP["/tools/get_user_requests/api/*"]
	// The current FileIO page mixes MCP browsing/mutations and dedicated HTTP
	// editing/history. GraphQL now covers the same operations, so the page is
	// available when either transport can serve it.
	mcpAndHTTP := mcpToolGroups(catalog)["file_io"] && catalog.HTTP["PUT /tools/file_io/api/file"]
	groups["file_io"] = mcpAndHTTP || graphql["file_io"]
	groups["memory"] = mcpToolGroups(catalog)["memory"] || graphql["memory"]
	return groups
}
