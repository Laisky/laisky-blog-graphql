package mcp

import (
	"context"
	"testing"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	ragplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/rag"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
)

type discoveryOnlyHistory struct{ calls int }

// ListVersionPage records accidental I/O during schema discovery without touching storage.
func (h *discoveryOnlyHistory) ListVersionPage(context.Context, files.AuthContext, string, string, uint64, int) (files.HistoryPage, error) {
	h.calls++
	return files.HistoryPage{}, errors.New("history I/O is not expected during discovery")
}

// ReadVersion records accidental I/O during schema discovery without touching storage.
func (h *discoveryOnlyHistory) ReadVersion(context.Context, files.AuthContext, string, string, uint64) (files.FileVersion, error) {
	h.calls++
	return files.FileVersion{}, errors.New("history I/O is not expected during discovery")
}

// TestHistoryToolsUseLiveRegistry exercises the real MCP server's serialized discovery response.
func TestHistoryToolsUseLiveRegistry(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		adapter, err := ragplugin.New(&files.Service{})
		require.NoError(t, err)
		history := &discoveryOnlyHistory{}
		server, err := NewServer(nil, nil, nil, nil, rag.Settings{}, adapter, nil, nil, nil,
			ToolsSettings{FileIOEnabled: enabled}, logSDK.Shared, WithFileHistoryReader(history))
		require.NoError(t, err)
		response := performModernMCPRequest(t, server.Handler(), mcpgo.MethodToolsList, nil)
		payload := decodeMCPResponse(t, response.Body.Bytes())
		result, ok := payload["result"].(map[string]any)
		require.True(t, ok, response.Body.String())
		definitions, ok := result["tools"].([]any)
		require.True(t, ok)
		for _, name := range []string{tools.FileListVersionsToolName, tools.FileReadVersionToolName, tools.FileRestoreVersionToolName} {
			require.Equal(t, enabled, toolListContains(definitions, name), name)
		}
		if enabled {
			for _, raw := range definitions {
				definition := raw.(map[string]any)
				if definition["name"] != tools.FileRestoreVersionToolName {
					continue
				}
				schema := definition["inputSchema"].(map[string]any)
				require.Len(t, schema["oneOf"], 2, "restore cannot advertise a blind write")
				properties := schema["properties"].(map[string]any)
				require.Equal(t, "string", properties["history_id"].(map[string]any)["type"])
				require.Equal(t, "string", properties["expected_version"].(map[string]any)["type"])
			}
		}
		require.Zero(t, history.calls)
	}
}
