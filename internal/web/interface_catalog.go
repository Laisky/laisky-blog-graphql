package web

// interfaceSources records independently constructed services and the actual MCP registry.
// It deliberately contains no MCP registration flags for GraphQL or HTTP decisions.
type interfaceSources struct {
	MCP                                          []string
	Search, Fetch, Extraction, Files, FileWriter bool
	AskUser, UserRequests, CallLogs              bool
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
		GraphQL: map[string]bool{
			"WebSearch": source.Search, "WebFetch": source.Fetch, "ExtractKeyInfo": source.Extraction,
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
		"web_search", "web_fetch", "extract_key_info", "ask_user", "get_user_request", "find_tool", "mcp_pipe",
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
		"extract_key_info": catalog.MCP["extract_key_info"], "ask_user": catalog.MCP["ask_user"],
		"get_user_request": catalog.MCP["get_user_request"],
		"file_io":          all("file_stat", "file_read", "file_write", "file_delete", "file_rename", "file_list", "file_search"),
		"memory":           all("memory_before_turn", "memory_after_turn", "memory_run_maintenance", "memory_list_dir_with_abstract"),
	}
}

// consoleGroups follows each page's actual transport, never a supposed preferred entrypoint.
func consoleGroups(catalog interfaceCatalog) map[string]bool {
	groups := consoleToolAvailability(mcpToolGroups(catalog), map[string]bool{
		"web_search": catalog.GraphQL["WebSearch"], "web_fetch": catalog.GraphQL["WebFetch"],
		"extract_key_info": catalog.GraphQL["ExtractKeyInfo"],
	})
	groups["ask_user"] = catalog.HTTP["/tools/ask_user/*"]
	groups["get_user_request"] = catalog.HTTP["/tools/get_user_requests/api/*"]
	// The current FileIO page mixes MCP browsing/mutations and dedicated HTTP editing/history.
	groups["file_io"] = groups["file_io"] && catalog.HTTP["PUT /tools/file_io/api/file"]
	return groups
}
