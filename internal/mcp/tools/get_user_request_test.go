package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/errors/v2"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/userrequests"
)

func TestGetUserRequestToolSuccess(t *testing.T) {
	consumedAt := time.Date(2025, time.January, 10, 8, 30, 0, 0, time.UTC)
	service := &fakeUserRequestService{
		consumeAll: func(context.Context, *askuser.AuthorizationContext) ([]userrequests.Request, error) {
			return []userrequests.Request{
				{
					ID:           testUUID("11111111-1111-1111-1111-111111111111"),
					Content:      "First command",
					Status:       userrequests.StatusConsumed,
					TaskID:       "default",
					UserIdentity: "user-alpha",
					CreatedAt:    consumedAt.Add(-2 * time.Hour),
					ConsumedAt:   &consumedAt,
				},
				{
					ID:           testUUID("22222222-2222-2222-2222-222222222222"),
					Content:      "Second command",
					Status:       userrequests.StatusConsumed,
					TaskID:       "default",
					UserIdentity: "user-alpha",
					CreatedAt:    consumedAt.Add(-time.Hour),
					ConsumedAt:   &consumedAt,
				},
			}, nil
		},
	}

	tool := mustGetUserRequestTool(t, service, func(context.Context) string { return "Bearer token" }, func(string) (*askuser.AuthorizationContext, error) {
		return &askuser.AuthorizationContext{UserIdentity: "user-alpha", KeySuffix: "abcd"}, nil
	})

	result, err := tool.Handle(context.Background(), mcp.CallToolRequest{})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.NotEmpty(t, result.Content)

	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)

	payload := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(text.Text), &payload))

	// Verify commands list is returned
	commands, ok := payload["commands"].([]any)
	require.True(t, ok, "commands should be a list")
	require.Len(t, commands, 2)

	// Verify first command (FIFO order)
	cmd1, ok := commands[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "First command", cmd1["content"])

	// Verify second command
	cmd2, ok := commands[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "Second command", cmd2["content"])

	// Verify auxiliary metadata is not included
	require.NotContains(t, cmd1, "request_id")
	require.NotContains(t, cmd1, "status")
	require.NotContains(t, cmd1, "task_id")
}

func TestGetUserRequestToolEmpty(t *testing.T) {
	service := &fakeUserRequestService{
		consumeAll: func(context.Context, *askuser.AuthorizationContext) ([]userrequests.Request, error) {
			return nil, userrequests.ErrNoPendingRequests
		},
	}

	tool := mustGetUserRequestTool(t, service, func(context.Context) string { return "Bearer token" }, func(string) (*askuser.AuthorizationContext, error) {
		return &askuser.AuthorizationContext{UserIdentity: "user-bravo"}, nil
	})

	result, err := tool.Handle(context.Background(), mcp.CallToolRequest{})
	require.NoError(t, err)
	require.False(t, result.IsError)

	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)
	require.Contains(t, text.Text, "empty")
}

func TestGetUserRequestToolAuthorizationFailure(t *testing.T) {
	tool := mustGetUserRequestTool(t, &fakeUserRequestService{}, func(context.Context) string { return "token" }, func(string) (*askuser.AuthorizationContext, error) {
		return nil, errors.New("invalid header")
	})

	result, err := tool.Handle(context.Background(), mcp.CallToolRequest{})
	require.NoError(t, err)
	require.True(t, result.IsError)

	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)
	require.Equal(t, "invalid authorization header", text.Text)
}

func mustGetUserRequestTool(t *testing.T, service UserRequestService, header AuthorizationHeaderProvider, parser AuthorizationParser) *GetUserRequestTool {
	t.Helper()
	tool, err := NewGetUserRequestTool(service, nil, testLogger(), header, parser)
	require.NoError(t, err)
	return tool
}

type fakeUserRequestService struct {
	consumeAll func(context.Context, *askuser.AuthorizationContext) ([]userrequests.Request, error)
}

func (f *fakeUserRequestService) ConsumeAllPending(ctx context.Context, auth *askuser.AuthorizationContext) ([]userrequests.Request, error) {
	if f.consumeAll != nil {
		return f.consumeAll(ctx, auth)
	}
	return nil, errors.New("not implemented")
}

func testUUID(value string) uuid.UUID {
	id, err := uuid.Parse(value)
	if err != nil {
		panic(err)
	}
	return id
}
