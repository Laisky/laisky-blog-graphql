package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/userrequests"
)

// fakeImageIssuer answers PresignURL and FetchInline from in-memory state.
type fakeImageIssuer struct {
	bodies map[string][]byte
}

func (f *fakeImageIssuer) PresignURL(_ context.Context, image userrequests.RequestImage) (string, error) {
	return fmt.Sprintf("https://fake-s3.local/%s?sig=abc", image.StorageKey), nil
}

func (f *fakeImageIssuer) FetchInline(_ context.Context, image userrequests.RequestImage) ([]byte, error) {
	body, ok := f.bodies[image.StorageKey]
	if !ok {
		return nil, fmt.Errorf("no inline bytes for %q", image.StorageKey)
	}
	return body, nil
}

func TestBuildCommandsResponse_PureTextCompatibility(t *testing.T) {
	tool, err := NewGetUserRequestTool(&fakeUserRequestService{}, nil, testLogger(), func(context.Context) string { return "" }, func(string) (*askuser.AuthorizationContext, error) { return nil, nil })
	require.NoError(t, err)

	req := userrequests.Request{
		ID:      uuid.New(),
		Content: "hello",
	}
	result, err := tool.buildCommandsResponse(context.Background(), []userrequests.Request{req})
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text.Text), &payload))
	commands, ok := payload["commands"].([]any)
	require.True(t, ok)
	require.Len(t, commands, 1)
}

func TestBuildCommandsResponse_WithImages(t *testing.T) {
	tool, err := NewGetUserRequestTool(&fakeUserRequestService{}, nil, testLogger(), func(context.Context) string { return "" }, func(string) (*askuser.AuthorizationContext, error) { return nil, nil })
	require.NoError(t, err)

	inlineBody := []byte("fake-png-bytes-small")
	issuer := &fakeImageIssuer{
		bodies: map[string][]byte{
			"mcp/images/u/a1.png": inlineBody,
		},
	}
	tool.WithImageIssuer(issuer)

	req := userrequests.Request{
		ID:      uuid.New(),
		Content: "analyze this",
		Images: []userrequests.RequestImage{
			{
				ID:         uuid.New(),
				StorageKey: "mcp/images/u/a1.png",
				SHA256:     "a1",
				SizeBytes:  int64(len(inlineBody)),
				MIMEType:   "image/png",
				Width:      100,
				Height:     50,
				ExpiresAt:  time.Now().Add(time.Hour),
			},
		},
	}

	result, err := tool.buildCommandsResponse(context.Background(), []userrequests.Request{req})
	require.NoError(t, err)
	require.NotEmpty(t, result.StructuredContent)

	require.GreaterOrEqual(t, len(result.Content), 3, "expected text + image + link")
	_, isText := result.Content[0].(mcp.TextContent)
	require.True(t, isText)
	_, isImage := result.Content[1].(mcp.ImageContent)
	require.True(t, isImage, "second block should be inlined ImageContent")
	link, isLink := result.Content[2].(mcp.ResourceLink)
	require.True(t, isLink)
	require.Contains(t, link.URI, "https://fake-s3.local/")

	structured, ok := result.StructuredContent.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "v2", structured["protocol_version"])
}

func TestBuildCommandsResponse_LargeImageGoesLinkOnly(t *testing.T) {
	tool, err := NewGetUserRequestTool(&fakeUserRequestService{}, nil, testLogger(), func(context.Context) string { return "" }, func(string) (*askuser.AuthorizationContext, error) { return nil, nil })
	require.NoError(t, err)

	issuer := &fakeImageIssuer{bodies: map[string][]byte{}}
	tool.WithImageIssuer(issuer)
	// Ensure per-image ceiling forces degraded (link-only) output regardless
	// of what FetchInline returns.
	tool.budget = ImageBudgetConfig{PerCallBudgetBytes: 10, PerImageInlineMax: 10}

	req := userrequests.Request{
		ID:      uuid.New(),
		Content: "too big to inline",
		Images: []userrequests.RequestImage{
			{
				ID:         uuid.New(),
				StorageKey: "mcp/images/u/big.png",
				SHA256:     "big",
				SizeBytes:  500 * 1024,
				MIMEType:   "image/png",
			},
		},
	}
	result, err := tool.buildCommandsResponse(context.Background(), []userrequests.Request{req})
	require.NoError(t, err)
	// Content order: TextContent then ResourceLink (no ImageContent because inline budget refused).
	require.Len(t, result.Content, 2)
	_, isText := result.Content[0].(mcp.TextContent)
	require.True(t, isText)
	_, isLink := result.Content[1].(mcp.ResourceLink)
	require.True(t, isLink)
}
