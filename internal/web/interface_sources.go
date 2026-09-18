package web

// interfaceSourcesFromResolver reads application dependencies independently of MCP flags.
// Registered MCP names are supplied by the actual constructed transport, possibly empty.
func interfaceSourcesFromResolver(resolver *Resolver, registered []string) interfaceSources {
	source := interfaceSources{MCP: registered}
	if resolver == nil {
		return source
	}
	source.Search = resolver.args.WebSearchProvider != nil
	source.Fetch = resolver.args.Rdb != nil
	source.Extraction = resolver.args.RAGService != nil
	source.Files = resolver.args.FilesService != nil
	source.FileWriter = resolver.args.MCPFileService != nil
	source.AskUser = resolver.args.AskUserService != nil
	source.UserRequests = resolver.args.UserRequestService != nil
	source.CallLogs = resolver.args.CallLogService != nil
	return source
}
