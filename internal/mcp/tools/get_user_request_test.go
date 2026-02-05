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
		consumeAll: func(context.Context, *askuser.AuthorizationContext, string) ([]userrequests.Request, error) {
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

	tool := mustGetUserRequestTool(t, service, nil, func(context.Context) string { return "Bearer token" }, func(string) (*askuser.AuthorizationContext, error) {
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
		consumeAll: func(context.Context, *askuser.AuthorizationContext, string) ([]userrequests.Request, error) {
			return nil, userrequests.ErrNoPendingRequests
		},
	}

	tool := mustGetUserRequestTool(t, service, nil, func(context.Context) string { return "Bearer token" }, func(string) (*askuser.AuthorizationContext, error) {
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
	tool := mustGetUserRequestTool(t, &fakeUserRequestService{}, nil, func(context.Context) string { return "token" }, func(string) (*askuser.AuthorizationContext, error) {
		return nil, errors.New("invalid header")
	})

	result, err := tool.Handle(context.Background(), mcp.CallToolRequest{})
	require.NoError(t, err)
	require.True(t, result.IsError)

	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)
	require.Equal(t, "invalid authorization header", text.Text)
}

func TestGetUserRequestToolTaskIDForwarding(t *testing.T) {
	capturedTaskID := ""
	service := &fakeUserRequestService{
		consumeAll: func(ctx context.Context, auth *askuser.AuthorizationContext, taskID string) ([]userrequests.Request, error) {
			capturedTaskID = taskID
			return nil, userrequests.ErrNoPendingRequests
		},
		getReturnMode: func(context.Context, *askuser.AuthorizationContext) (string, error) {
			return "all", nil
		},
	}

	tool := mustGetUserRequestTool(t, service, nil, func(context.Context) string { return "Bearer token" }, func(string) (*askuser.AuthorizationContext, error) {
		return &askuser.AuthorizationContext{UserIdentity: "user-task", KeySuffix: "abcd"}, nil
	})

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"task_id": "workspace-42"}}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, "workspace-42", capturedTaskID)
}

func mustGetUserRequestTool(t *testing.T, service UserRequestService, hold HoldWaiter, header AuthorizationHeaderProvider, parser AuthorizationParser) *GetUserRequestTool {
	t.Helper()
	tool, err := NewGetUserRequestTool(service, hold, testLogger(), header, parser)
	require.NoError(t, err)
	return tool
}

func TestGetUserRequestToolHoldTimeout(t *testing.T) {
	// When hold is active and there are no pending commands, the tool waits.
	// If the wait times out, it should return a hold_timeout status.
	service := &fakeUserRequestService{
		consumeAll: func(context.Context, *askuser.AuthorizationContext, string) ([]userrequests.Request, error) {
			// Return no pending requests so the tool proceeds to wait
			return nil, userrequests.ErrNoPendingRequests
		},
	}

	waiter := &fakeHoldWaiter{
		active: true,
		wait: func(context.Context, string, string) (*userrequests.Request, bool) {
			return nil, true // Simulate timeout
		},
	}

	tool := mustGetUserRequestTool(t, service, waiter, func(context.Context) string { return "Bearer token" }, func(string) (*askuser.AuthorizationContext, error) {
		return &askuser.AuthorizationContext{UserIdentity: "user-hold"}, nil
	})

	result, err := tool.Handle(context.Background(), mcp.CallToolRequest{})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.NotEmpty(t, result.Content)

	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)
	payload := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(text.Text), &payload))
	require.Equal(t, "hold_timeout", payload["status"])
	require.Contains(t, payload["message"], "typing")
	require.Contains(t, payload["message"], "get_user_request")
}

func TestGetUserRequestToolHoldWithPendingCommands(t *testing.T) {
	// When hold is active but there are already pending commands,
	// the tool should return them immediately without waiting.
	consumedAt := time.Date(2025, time.January, 10, 8, 30, 0, 0, time.UTC)
	service := &fakeUserRequestService{
		consumeAll: func(context.Context, *askuser.AuthorizationContext, string) ([]userrequests.Request, error) {
			return []userrequests.Request{
				{
					ID:           testUUID("33333333-3333-3333-3333-333333333333"),
					Content:      "Existing pending command",
					Status:       userrequests.StatusConsumed,
					TaskID:       "default",
					UserIdentity: "user-hold-pending",
					CreatedAt:    consumedAt.Add(-time.Hour),
					ConsumedAt:   &consumedAt,
				},
			}, nil
		},
	}

	waiter := &fakeHoldWaiter{
		active: true,
		wait: func(context.Context, string, string) (*userrequests.Request, bool) {
			require.Fail(t, "wait should not be called when pending commands exist")
			return nil, false
		},
	}

	tool := mustGetUserRequestTool(t, service, waiter, func(context.Context) string { return "Bearer token" }, func(string) (*askuser.AuthorizationContext, error) {
		return &askuser.AuthorizationContext{UserIdentity: "user-hold-pending"}, nil
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
	require.Len(t, commands, 1)

	// Verify the command content
	cmd, ok := commands[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "Existing pending command", cmd["content"])
}

type fakeUserRequestService struct {
	consumeAll    func(context.Context, *askuser.AuthorizationContext, string) ([]userrequests.Request, error)
	consumeFirst  func(context.Context, *askuser.AuthorizationContext, string) (*userrequests.Request, error)
	getReturnMode func(context.Context, *askuser.AuthorizationContext) (string, error)
}

func (f *fakeUserRequestService) ConsumeAllPending(ctx context.Context, auth *askuser.AuthorizationContext, taskID string) ([]userrequests.Request, error) {
	if f.consumeAll != nil {
		return f.consumeAll(ctx, auth, taskID)
	}
	return nil, errors.New("not implemented")
}

func (f *fakeUserRequestService) ConsumeFirstPending(ctx context.Context, auth *askuser.AuthorizationContext, taskID string) (*userrequests.Request, error) {
	if f.consumeFirst != nil {
		return f.consumeFirst(ctx, auth, taskID)
	}
	// Default: return first from consumeAll if available
	if f.consumeAll != nil {
		requests, err := f.consumeAll(ctx, auth, taskID)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if len(requests) > 0 {
			return &requests[0], nil
		}
		return nil, userrequests.ErrNoPendingRequests
	}
	return nil, errors.New("not implemented")
}

func (f *fakeUserRequestService) GetReturnMode(ctx context.Context, auth *askuser.AuthorizationContext) (string, error) {
	if f.getReturnMode != nil {
		return f.getReturnMode(ctx, auth)
	}
	// Default to "all" mode
	return "all", nil
}

type fakeHoldWaiter struct {
	active bool
	wait   func(ctx context.Context, apiKeyHash string, taskID string) (*userrequests.Request, bool)
}

func (f *fakeHoldWaiter) IsHoldActive(string, string) bool {
	return f.active
}

func (f *fakeHoldWaiter) WaitForCommand(ctx context.Context, apiKeyHash string, taskID string) (*userrequests.Request, bool) {
	if f.wait != nil {
		return f.wait(ctx, apiKeyHash, taskID)
	}
	return nil, false
}

func testUUID(value string) uuid.UUID {
	id, err := uuid.Parse(value)
	if err != nil {
		panic(err)
	}
	return id
}

// TestGetUserRequestToolIgnoresAgentReturnMode ensures agent-supplied return_mode is ignored.
func TestGetUserRequestToolIgnoresAgentReturnMode(t *testing.T) {
	consumedAt := time.Date(2025, time.January, 10, 8, 30, 0, 0, time.UTC)
	consumeFirstCalled := false
	consumeAllCalled := false

	service := &fakeUserRequestService{
		consumeFirst: func(context.Context, *askuser.AuthorizationContext, string) (*userrequests.Request, error) {
			consumeFirstCalled = true
			return &userrequests.Request{ID: testUUID("99999999-9999-9999-9999-999999999999")}, nil
		},
		consumeAll: func(context.Context, *askuser.AuthorizationContext, string) ([]userrequests.Request, error) {
			consumeAllCalled = true
			return []userrequests.Request{{
				ID:           testUUID("11111111-1111-1111-1111-111111111111"),
				Content:      "First command only",
				Status:       userrequests.StatusConsumed,
				TaskID:       "default",
				UserIdentity: "user-first",
				CreatedAt:    consumedAt.Add(-2 * time.Hour),
				ConsumedAt:   &consumedAt,
			}}, nil
		},
	}

	tool := mustGetUserRequestTool(t, service, nil, func(context.Context) string { return "Bearer token" }, func(string) (*askuser.AuthorizationContext, error) {
		return &askuser.AuthorizationContext{UserIdentity: "user-first", KeySuffix: "abcd"}, nil
	})

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"return_mode": "first", // should be ignored
	}

	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.NotEmpty(t, result.Content)

	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)

	payload := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(text.Text), &payload))

	// Verify commands list is returned using default "all" mode
	commands, ok := payload["commands"].([]any)
	require.True(t, ok, "commands should be a list")
	require.Len(t, commands, 1, "should return commands from consumeAll")

	// Verify consumeAll was called, agent override ignored
	require.True(t, consumeAllCalled, "ConsumeAllPending should be called when preference is 'all'")
	require.False(t, consumeFirstCalled, "ConsumeFirstPending should be ignored when agent tries to override")
}

// TestGetUserRequestToolReturnModeFromUserPreference verifies that when no return_mode
// is specified by the agent, the tool uses the user's stored preference.
func TestGetUserRequestToolReturnModeFromUserPreference(t *testing.T) {
	consumedAt := time.Date(2025, time.January, 10, 8, 30, 0, 0, time.UTC)
	consumeFirstCalled := false

	service := &fakeUserRequestService{
		consumeFirst: func(context.Context, *askuser.AuthorizationContext, string) (*userrequests.Request, error) {
			consumeFirstCalled = true
			return &userrequests.Request{
				ID:           testUUID("11111111-1111-1111-1111-111111111111"),
				Content:      "First from preference",
				Status:       userrequests.StatusConsumed,
				TaskID:       "default",
				UserIdentity: "user-pref",
				CreatedAt:    consumedAt.Add(-2 * time.Hour),
				ConsumedAt:   &consumedAt,
			}, nil
		},
		// User preference is set to "first"
		getReturnMode: func(context.Context, *askuser.AuthorizationContext) (string, error) {
			return "first", nil
		},
	}

	tool := mustGetUserRequestTool(t, service, nil, func(context.Context) string { return "Bearer token" }, func(string) (*askuser.AuthorizationContext, error) {
		return &askuser.AuthorizationContext{UserIdentity: "user-pref", KeySuffix: "abcd"}, nil
	})

	// Call without specifying return_mode - should use user preference
	result, err := tool.Handle(context.Background(), mcp.CallToolRequest{})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.NotEmpty(t, result.Content)

	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)

	payload := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(text.Text), &payload))

	// Verify only one command is returned (using "first" mode from preference)
	commands, ok := payload["commands"].([]any)
	require.True(t, ok, "commands should be a list")
	require.Len(t, commands, 1, "should return exactly one command based on user preference")

	// Verify consumeFirst was called
	require.True(t, consumeFirstCalled, "ConsumeFirstPending should be called based on user preference")
}

// TestGetUserRequestToolAgentModeDoesNotOverridePreference verifies agent hints are ignored in favor of user preference.
func TestGetUserRequestToolAgentModeDoesNotOverridePreference(t *testing.T) {
	consumedAt := time.Date(2025, time.January, 10, 8, 30, 0, 0, time.UTC)
	consumeAllCalled := false
	consumeFirstCalled := false

	service := &fakeUserRequestService{
		consumeAll: func(context.Context, *askuser.AuthorizationContext, string) ([]userrequests.Request, error) {
			consumeAllCalled = true
			return []userrequests.Request{{
				ID:           testUUID("11111111-1111-1111-1111-111111111111"),
				Content:      "First command",
				Status:       userrequests.StatusConsumed,
				TaskID:       "default",
				UserIdentity: "user-override",
				CreatedAt:    consumedAt.Add(-2 * time.Hour),
				ConsumedAt:   &consumedAt,
			}}, nil
		},
		consumeFirst: func(context.Context, *askuser.AuthorizationContext, string) (*userrequests.Request, error) {
			consumeFirstCalled = true
			return &userrequests.Request{
				ID:           testUUID("33333333-3333-3333-3333-333333333333"),
				Content:      "First only",
				Status:       userrequests.StatusConsumed,
				TaskID:       "default",
				UserIdentity: "user-override",
				CreatedAt:    consumedAt.Add(-2 * time.Hour),
				ConsumedAt:   &consumedAt,
			}, nil
		},
		// User preference is set to "first"; agent hints should not override.
		getReturnMode: func(context.Context, *askuser.AuthorizationContext) (string, error) {
			return "first", nil
		},
	}

	tool := mustGetUserRequestTool(t, service, nil, func(context.Context) string { return "Bearer token" }, func(string) (*askuser.AuthorizationContext, error) {
		return &askuser.AuthorizationContext{UserIdentity: "user-override", KeySuffix: "abcd"}, nil
	})

	// Agent explicitly specifies return_mode=all, but preference remains "first"
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"return_mode": "all",
	}

	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.NotEmpty(t, result.Content)

	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)

	payload := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(text.Text), &payload))

	// Verify only one command is returned (user preference "first" wins)
	commands, ok := payload["commands"].([]any)
	require.True(t, ok, "commands should be a list")
	require.Len(t, commands, 1, "should return a single command based on user preference")

	// Verify consumeFirst was called and agent override ignored
	require.True(t, consumeFirstCalled, "ConsumeFirstPending should be called when preference is 'first'")
	require.False(t, consumeAllCalled, "ConsumeAllPending should not be called when agent tries to override")
}

// TestGetUserRequestToolReturnModeFirstEmpty verifies that when return_mode=first
// and there are no pending requests, the empty response is returned correctly.
func TestGetUserRequestToolReturnModeFirstEmpty(t *testing.T) {
	service := &fakeUserRequestService{
		consumeFirst: func(context.Context, *askuser.AuthorizationContext, string) (*userrequests.Request, error) {
			return nil, userrequests.ErrNoPendingRequests
		},
		getReturnMode: func(context.Context, *askuser.AuthorizationContext) (string, error) {
			return "first", nil
		},
	}

	tool := mustGetUserRequestTool(t, service, nil, func(context.Context) string { return "Bearer token" }, func(string) (*askuser.AuthorizationContext, error) {
		return &askuser.AuthorizationContext{UserIdentity: "user-empty"}, nil
	})

	result, err := tool.Handle(context.Background(), mcp.CallToolRequest{})
	require.NoError(t, err)
	require.False(t, result.IsError)

	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)
	require.Contains(t, text.Text, "empty")
}

// TestGetUserRequestToolReturnModeFirstWithHold verifies that when hold is active
// and return_mode=first, only the first pending command is returned.
func TestGetUserRequestToolReturnModeFirstWithHold(t *testing.T) {
	consumedAt := time.Date(2025, time.January, 10, 8, 30, 0, 0, time.UTC)
	consumeFirstCalled := false

	service := &fakeUserRequestService{
		consumeFirst: func(context.Context, *askuser.AuthorizationContext, string) (*userrequests.Request, error) {
			consumeFirstCalled = true
			return &userrequests.Request{
				ID:           testUUID("44444444-4444-4444-4444-444444444444"),
				Content:      "First during hold",
				Status:       userrequests.StatusConsumed,
				TaskID:       "default",
				UserIdentity: "user-hold-first",
				CreatedAt:    consumedAt.Add(-time.Hour),
				ConsumedAt:   &consumedAt,
			}, nil
		},
		getReturnMode: func(context.Context, *askuser.AuthorizationContext) (string, error) {
			return "first", nil
		},
	}

	waiter := &fakeHoldWaiter{
		active: true,
		wait: func(context.Context, string, string) (*userrequests.Request, bool) {
			require.Fail(t, "wait should not be called when pending commands exist")
			return nil, false
		},
	}

	tool := mustGetUserRequestTool(t, service, waiter, func(context.Context) string { return "Bearer token" }, func(string) (*askuser.AuthorizationContext, error) {
		return &askuser.AuthorizationContext{UserIdentity: "user-hold-first"}, nil
	})

	result, err := tool.Handle(context.Background(), mcp.CallToolRequest{})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.NotEmpty(t, result.Content)

	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)

	payload := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(text.Text), &payload))

	// Verify only one command is returned
	commands, ok := payload["commands"].([]any)
	require.True(t, ok, "commands should be a list")
	require.Len(t, commands, 1, "should return exactly one command with hold active")

	// Verify consumeFirst was called
	require.True(t, consumeFirstCalled, "ConsumeFirstPending should be called when return_mode=first with hold")
}
