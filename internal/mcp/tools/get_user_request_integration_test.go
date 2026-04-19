package tools

import (
	"bytes"
	"context"
	"database/sql"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/storage"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/userrequests"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// TestIntegration_UploadThenGetUserRequest exercises the full pipeline end-to-end
// against in-memory stores: multipart POST ➜ image normalization ➜ fake MinIO ➜
// DB persist ➜ MCP tool builds a mixed-content response.
func TestIntegration_UploadThenGetUserRequest(t *testing.T) {
	db, cleanup := openIntegrationDB(t)
	defer cleanup()

	clock := func() time.Time { return time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC) }
	svc, err := userrequests.NewService(db, nil, clock, userrequests.Settings{
		RetentionDays: userrequests.DefaultRetentionDays,
		Images: userrequests.ImageSettings{
			Enabled:           true,
			Prefix:            "mcp/images",
			PerUserQuotaBytes: 10 * 1024 * 1024,
			PerImageMaxBytes:  20 * 1024 * 1024,
			MaxPerRequest:     5,
			ObjectTTLDays:     7,
			PresignTTL:        30 * time.Minute,
		},
	})
	require.NoError(t, err)

	store := storage.NewFakeStore()
	imageManager := userrequests.NewImageManager(store, nil, svc.ImageSettings())
	handler := userrequests.NewCombinedHTTPHandlerWithImages(svc, nil, imageManager, log.Logger, nil)

	// Upload a real PNG via the HTTP handler.
	img := image.NewRGBA(image.Rect(0, 0, 80, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 80; x++ {
			img.Set(x, y, color.RGBA{R: 100, G: 150, B: 200, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	require.NoError(t, png.Encode(&pngBuf, img))

	buf, contentType := buildMultipartForm(t, "review this diagram", "default", "diagram.png", pngBuf.Bytes())
	req := httptest.NewRequest(http.MethodPost, "/api/requests", buf)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer sk-integration-test-9a9a9a9a9a")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, 1, store.ObjectCount())

	// Authenticate as the same caller to pull the request through the tool.
	auth, err := askuser.ParseAuthorizationContext("Bearer sk-integration-test-9a9a9a9a9a")
	require.NoError(t, err)

	tool, err := NewGetUserRequestTool(svc, nil, testLogger(), func(context.Context) string { return auth.RawHeader }, askuser.ParseAuthorizationContext)
	require.NoError(t, err)
	tool.WithImageIssuer(imageManager)

	callReq := mcp.CallToolRequest{}
	callReq.Params.Arguments = map[string]any{}
	result, err := tool.Handle(context.Background(), callReq)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Expect TextContent + ImageContent + ResourceLink in order.
	require.GreaterOrEqual(t, len(result.Content), 3)
	_, isText := result.Content[0].(mcp.TextContent)
	require.True(t, isText)
	_, isImage := result.Content[1].(mcp.ImageContent)
	require.True(t, isImage, "small PNG should inline")
	link, isLink := result.Content[2].(mcp.ResourceLink)
	require.True(t, isLink)
	require.Contains(t, link.URI, "fake-s3")

	structured, ok := result.StructuredContent.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "v2", structured["protocol_version"])
}

func openIntegrationDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:integration_test_images?mode=memory&cache=shared")
	require.NoError(t, err)
	return db, func() { _ = db.Close() }
}

func buildMultipartForm(t *testing.T, content, taskID, filename string, body []byte) (*bytes.Buffer, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	writer := newMultipartWriter(buf)
	require.NoError(t, writer.WriteField("content", content))
	require.NoError(t, writer.WriteField("task_id", taskID))
	part, err := writer.CreateImagePart(filename, "image/png")
	require.NoError(t, err)
	_, err = part.Write(body)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return buf, writer.ContentType()
}
