package mcp

import (
	"context"

	errors "github.com/Laisky/errors/v2"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	srv "github.com/mark3labs/mcp-go/server"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/userrequests"
)

// ServerOption injects shared application services without letting MCP own another interface.
type ServerOption func(*sharedServerServices)

type sharedServerServices struct {
	history files.HistoryReader
	holds   *userrequests.HoldManager
}

// WithFileHistoryReader enables the history tool adapters when FileIO exposure is enabled.
func WithFileHistoryReader(reader files.HistoryReader) ServerOption {
	return func(services *sharedServerServices) { services.history = reader }
}

// WithUserRequestHoldManager shares the application's hold manager with HTTP and MCP clients.
func WithUserRequestHoldManager(manager *userrequests.HoldManager) ServerOption {
	return func(services *sharedServerServices) { services.holds = manager }
}

// registerFileHistoryTools applies the same audit/billing wrapper and discovery registration as other tools.
func (s *Server) registerFileHistoryTools(server *srv.MCPServer, writer tools.FileService, reader files.HistoryReader) error {
	if reader == nil {
		return nil
	}
	historyTools, err := tools.NewFileHistoryTools(writer, reader)
	if err != nil {
		return errors.Wrap(err, "initialize file history tools")
	}
	for _, tool := range historyTools {
		definition := tool.Definition()
		execute := tool.Handle
		s.registerTool(server, definition, func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return s.executeToolHandler(ctx, request, definition.Name, 0, "file history is unavailable", execute)
		})
	}
	return nil
}
