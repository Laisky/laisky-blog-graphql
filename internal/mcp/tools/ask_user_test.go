package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v5"
	logSDK "github.com/Laisky/go-utils/v5/log"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/library/log"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

func TestAskUserHandleInvalidAuthorization(t *testing.T) {
	parserErr := errors.New("invalid header")
	tool := mustAskUserTool(t, &fakeAskUserService{}, func(context.Context) string { return "token" }, func(string) (*askuser.AuthorizationContext, error) {
		return nil, parserErr
	}, 5*time.Minute)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"question": "What is the status?",
			},
		},
	}

	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.True(t, result.IsError)

	textContent, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)
	require.Equal(t, "invalid authorization header", textContent.Text)
}

func TestAskUserHandleEmptyQuestion(t *testing.T) {
	tool := mustAskUserTool(t, &fakeAskUserService{}, func(context.Context) string { return "token" }, func(string) (*askuser.AuthorizationContext, error) {
		return &askuser.AuthorizationContext{}, nil
	}, 5*time.Minute)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"question": "   ",
			},
		},
	}

	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.True(t, result.IsError)

	textContent, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)
	require.Equal(t, "question cannot be empty", textContent.Text)
}

func TestAskUserHandleTimeout(t *testing.T) {
	cancelCalled := false
	storedID := gutils.UUID7Bytes()
	service := &fakeAskUserService{
		create: func(context.Context, *askuser.AuthorizationContext, string) (*askuser.Request, error) {
			return &askuser.Request{ID: storedID, Question: "Ping"}, nil
		},
		wait: func(context.Context, uuid.UUID) (*askuser.Request, error) {
			return nil, context.DeadlineExceeded
		},
		cancel: func(ctx context.Context, id uuid.UUID, status string) error {
			cancelCalled = true
			require.Equal(t, storedID, id)
			require.Equal(t, askuser.StatusExpired, status)
			return nil
		},
	}

	tool := mustAskUserTool(t, service, func(context.Context) string { return "Bearer token" }, func(string) (*askuser.AuthorizationContext, error) {
		return &askuser.AuthorizationContext{}, nil
	}, time.Millisecond)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"question": "Ping?",
			},
		},
	}

	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.True(t, cancelCalled)

	textContent, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)
	require.Equal(t, "timeout waiting for user response", textContent.Text)
}

func TestAskUserHandleSuccess(t *testing.T) {
	answer := "All good"
	answeredAt := time.Date(2025, time.October, 25, 12, 0, 0, 0, time.UTC)
	storedID := gutils.UUID7Bytes()
	service := &fakeAskUserService{
		create: func(context.Context, *askuser.AuthorizationContext, string) (*askuser.Request, error) {
			return &askuser.Request{ID: storedID, Question: "Ping"}, nil
		},
		wait: func(context.Context, uuid.UUID) (*askuser.Request, error) {
			return &askuser.Request{
				ID:         storedID,
				Question:   "Ping",
				Answer:     &answer,
				CreatedAt:  answeredAt.Add(-time.Minute),
				AnsweredAt: &answeredAt,
			}, nil
		},
		cancel: func(context.Context, uuid.UUID, string) error {
			return nil
		},
	}

	tool := mustAskUserTool(t, service, func(context.Context) string { return "Bearer token" }, func(string) (*askuser.AuthorizationContext, error) {
		return &askuser.AuthorizationContext{APIKey: "token"}, nil
	}, 5*time.Minute)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"question": "Ping?",
			},
		},
	}

	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	textContent, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)

	payload := make(map[string]any)
	require.NoError(t, json.Unmarshal([]byte(textContent.Text), &payload))
	require.Equal(t, storedID.String(), payload["request_id"])
	require.Equal(t, "Ping", payload["question"])
	require.Equal(t, answer, payload["answer"])
	require.Equal(t, answeredAt.Add(-time.Minute).Format(time.RFC3339Nano), payload["asked_at"])
	require.Equal(t, answeredAt.Format(time.RFC3339Nano), payload["answered_at"])
}

func mustAskUserTool(t *testing.T, service AskUserService, header AuthorizationHeaderProvider, parser AuthorizationParser, timeout time.Duration) *AskUserTool {
	t.Helper()

	tool, err := NewAskUserTool(service, testLogger(), header, parser, timeout)
	require.NoError(t, err)
	return tool
}

type fakeAskUserService struct {
	create func(context.Context, *askuser.AuthorizationContext, string) (*askuser.Request, error)
	wait   func(context.Context, uuid.UUID) (*askuser.Request, error)
	cancel func(context.Context, uuid.UUID, string) error
}

func (f *fakeAskUserService) CreateRequest(ctx context.Context, auth *askuser.AuthorizationContext, question string) (*askuser.Request, error) {
	if f.create != nil {
		return f.create(ctx, auth, question)
	}
	return nil, errors.New("create not implemented")
}

func (f *fakeAskUserService) WaitForAnswer(ctx context.Context, id uuid.UUID) (*askuser.Request, error) {
	if f.wait != nil {
		return f.wait(ctx, id)
	}
	return nil, errors.New("wait not implemented")
}

func (f *fakeAskUserService) CancelRequest(ctx context.Context, id uuid.UUID, status string) error {
	if f.cancel != nil {
		return f.cancel(ctx, id, status)
	}
	return nil
}

func testLogger() logSDK.Logger {
	return log.Logger.Named("test_ask_user")
}
