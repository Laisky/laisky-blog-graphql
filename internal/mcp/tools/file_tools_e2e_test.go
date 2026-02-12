package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

type e2eEmbedder struct{}

// EmbedTexts returns deterministic vectors for end-to-end tests.
func (e2eEmbedder) EmbedTexts(_ context.Context, _ string, inputs []string) ([]pgvector.Vector, error) {
	vectors := make([]pgvector.Vector, 0, len(inputs))
	for range inputs {
		vectors = append(vectors, pgvector.NewVector([]float32{1, 0}))
	}
	return vectors, nil
}

type e2eCredentialStore struct {
	data map[string]string
}

// Store stores encrypted credential envelopes in memory for tests.
func (s *e2eCredentialStore) Store(_ context.Context, key, payload string, _ time.Duration) error {
	if s.data == nil {
		s.data = map[string]string{}
	}
	s.data[key] = payload
	return nil
}

// Load loads encrypted credential envelopes from memory for tests.
func (s *e2eCredentialStore) Load(_ context.Context, key string) (string, error) {
	value, ok := s.data[key]
	if !ok {
		return "", gorm.ErrRecordNotFound
	}
	return value, nil
}

// Delete removes encrypted credential envelopes from memory for tests.
func (s *e2eCredentialStore) Delete(_ context.Context, key string) error {
	delete(s.data, key)
	return nil
}

// TestFileToolsEndToEndFlow verifies the full MCP tool flow for file operations.
func TestFileToolsEndToEndFlow(t *testing.T) {
	svc := newE2EFileService(t, false)
	authCtx := context.WithValue(context.Background(), ctxkeys.AuthContext, &files.AuthContext{
		APIKey:       "key",
		APIKeyHash:   "hash",
		UserIdentity: "user:test",
	})

	writeTool, err := NewFileWriteTool(svc)
	require.NoError(t, err)
	statTool, err := NewFileStatTool(svc)
	require.NoError(t, err)
	readTool, err := NewFileReadTool(svc)
	require.NoError(t, err)
	listTool, err := NewFileListTool(svc)
	require.NoError(t, err)
	searchTool, err := NewFileSearchTool(svc)
	require.NoError(t, err)
	deleteTool, err := NewFileDeleteTool(svc)
	require.NoError(t, err)

	writeResp, err := writeTool.Handle(authCtx, newToolReq(map[string]any{
		"project": "proj",
		"path":    "/docs/a.txt",
		"content": "hello tools",
		"mode":    "APPEND",
	}))
	require.NoError(t, err)
	require.False(t, writeResp.IsError)
	writePayload := decodeToolPayload(t, writeResp)
	require.Equal(t, 11, asInt(t, writePayload["bytes_written"]))

	statResp, err := statTool.Handle(authCtx, newToolReq(map[string]any{
		"project": "proj",
		"path":    "/docs/a.txt",
	}))
	require.NoError(t, err)
	require.False(t, statResp.IsError)
	statPayload := decodeToolPayload(t, statResp)
	require.Equal(t, true, statPayload["exists"])
	require.Equal(t, "FILE", statPayload["type"])

	readResp, err := readTool.Handle(authCtx, newToolReq(map[string]any{
		"project": "proj",
		"path":    "/docs/a.txt",
	}))
	require.NoError(t, err)
	require.False(t, readResp.IsError)
	readPayload := decodeToolPayload(t, readResp)
	require.Equal(t, "hello tools", readPayload["content"])
	require.Equal(t, "utf-8", readPayload["content_encoding"])

	listResp, err := listTool.Handle(authCtx, newToolReq(map[string]any{
		"project": "proj",
		"path":    "/docs",
		"depth":   1,
		"limit":   10,
	}))
	require.NoError(t, err)
	require.False(t, listResp.IsError)
	listPayload := decodeToolPayload(t, listResp)
	require.Equal(t, false, listPayload["has_more"])

	worker := svc.NewIndexWorker()
	require.NoError(t, worker.RunOnce(context.Background()))

	searchResp, err := searchTool.Handle(authCtx, newToolReq(map[string]any{
		"project": "proj",
		"query":   "hello",
		"limit":   5,
	}))
	require.NoError(t, err)
	require.False(t, searchResp.IsError)
	searchPayload := decodeToolPayload(t, searchResp)
	chunks, ok := searchPayload["chunks"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, chunks)

	deleteResp, err := deleteTool.Handle(authCtx, newToolReq(map[string]any{
		"project":   "proj",
		"path":      "/docs/a.txt",
		"recursive": false,
	}))
	require.NoError(t, err)
	require.False(t, deleteResp.IsError)
	deletePayload := decodeToolPayload(t, deleteResp)
	require.Equal(t, 1, asInt(t, deletePayload["deleted_count"]))

	statAfterDeleteResp, err := statTool.Handle(authCtx, newToolReq(map[string]any{
		"project": "proj",
		"path":    "/docs/a.txt",
	}))
	require.NoError(t, err)
	require.False(t, statAfterDeleteResp.IsError)
	statAfterDeletePayload := decodeToolPayload(t, statAfterDeleteResp)
	require.Equal(t, false, statAfterDeletePayload["exists"])
}

// TestFileDeleteToolRootWipe verifies root delete is denied even when wipe is enabled.
func TestFileDeleteToolRootWipe(t *testing.T) {
	svc := newE2EFileService(t, true)
	authCtx := context.WithValue(context.Background(), ctxkeys.AuthContext, &files.AuthContext{
		APIKey:       "key",
		APIKeyHash:   "hash",
		UserIdentity: "user:test",
	})

	writeTool, err := NewFileWriteTool(svc)
	require.NoError(t, err)
	deleteTool, err := NewFileDeleteTool(svc)
	require.NoError(t, err)
	listTool, err := NewFileListTool(svc)
	require.NoError(t, err)

	_, err = writeTool.Handle(authCtx, newToolReq(map[string]any{
		"project": "proj",
		"path":    "/a.txt",
		"content": "one",
	}))
	require.NoError(t, err)
	_, err = writeTool.Handle(authCtx, newToolReq(map[string]any{
		"project": "proj",
		"path":    "/b.txt",
		"content": "two",
	}))
	require.NoError(t, err)

	deleteResp, err := deleteTool.Handle(authCtx, newToolReq(map[string]any{
		"project":   "proj",
		"recursive": true,
	}))
	require.NoError(t, err)
	require.True(t, deleteResp.IsError)
	deletePayload := decodeToolPayload(t, deleteResp)
	require.Equal(t, string(files.ErrCodePermissionDenied), deletePayload["code"])

	listResp, err := listTool.Handle(authCtx, newToolReq(map[string]any{
		"project": "proj",
		"path":    "",
	}))
	require.NoError(t, err)
	require.False(t, listResp.IsError)
	listPayload := decodeToolPayload(t, listResp)
	require.Equal(t, false, listPayload["has_more"])
	entries, ok := listPayload["entries"].([]any)
	require.True(t, ok)
	require.Len(t, entries, 2)
}

// TestFileDeleteToolRootWipeDisabled verifies root delete remains blocked when wipe is disabled.
func TestFileDeleteToolRootWipeDisabled(t *testing.T) {
	svc := newE2EFileService(t, false)
	authCtx := context.WithValue(context.Background(), ctxkeys.AuthContext, &files.AuthContext{
		APIKey:       "key",
		APIKeyHash:   "hash",
		UserIdentity: "user:test",
	})

	deleteTool, err := NewFileDeleteTool(svc)
	require.NoError(t, err)

	deleteResp, err := deleteTool.Handle(authCtx, newToolReq(map[string]any{
		"project":   "proj",
		"recursive": true,
	}))
	require.NoError(t, err)
	require.True(t, deleteResp.IsError)
	deletePayload := decodeToolPayload(t, deleteResp)
	require.Equal(t, string(files.ErrCodePermissionDenied), deletePayload["code"])
}

// newE2EFileService creates a concrete file service for MCP tool integration tests.
func newE2EFileService(t *testing.T, allowRootWipe bool) *files.Service {
	t.Helper()

	settings := files.LoadSettingsFromConfig()
	settings.AllowRootWipe = allowRootWipe
	settings.Search.Enabled = true
	settings.MaxProjectBytes = 1_000_000
	settings.Index.BatchSize = 20
	settings.Index.ChunkBytes = 64
	settings.Security.EncryptionKey = base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))

	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared", t.Name(), time.Now().UTC().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)

	credential, err := files.NewCredentialProtector(settings.Security)
	require.NoError(t, err)

	svc, err := files.NewService(
		db,
		settings,
		e2eEmbedder{},
		nil,
		credential,
		&e2eCredentialStore{},
		nil,
		nil,
		func() time.Time { return time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC) },
	)
	require.NoError(t, err)

	return svc
}

// newToolReq builds a call request with argument payloads for MCP tool tests.
func newToolReq(args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}}
}

// decodeToolPayload decodes structured MCP tool responses into a generic map.
func decodeToolPayload(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()

	if result.StructuredContent != nil {
		data, err := json.Marshal(result.StructuredContent)
		require.NoError(t, err)
		payload := map[string]any{}
		require.NoError(t, json.Unmarshal(data, &payload))
		return payload
	}

	for _, content := range result.Content {
		text, ok := mcp.AsTextContent(content)
		if !ok {
			continue
		}
		payload := map[string]any{}
		require.NoError(t, json.Unmarshal([]byte(text.Text), &payload))
		return payload
	}

	require.FailNow(t, "tool response payload missing")
	return nil
}

// asInt converts JSON number representations into an int.
func asInt(t *testing.T, value any) int {
	t.Helper()
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	default:
		require.FailNow(t, "unsupported numeric type")
		return 0
	}
}
