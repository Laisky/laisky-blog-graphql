package userrequests

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// TestHoldHTTPCreateRequestAutoReleasesWhenAgentWaiting verifies that when hold is active,
// an agent is already waiting, and a new directive is created for the same task,
// the directive is delivered immediately and the hold is released automatically.
func TestHoldHTTPCreateRequestAutoReleasesWhenAgentWaiting(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	holdMgr := NewHoldManager(svc, log.Logger.Named("test_hold"), nil)
	handler := NewCombinedHTTPHandler(svc, holdMgr, log.Logger.Named("test_http"), nil)

	authHeader := "Bearer sk-test-hold-auto-release-123456"
	authCtx, err := askuser.ParseAuthorizationContext(authHeader)
	require.NoError(t, err)

	taskID := "task-auto"

	activateReq := httptest.NewRequest(http.MethodPost, "/api/hold?task_id="+taskID, nil)
	activateReq.Header.Set("Authorization", authHeader)
	activateRec := httptest.NewRecorder()
	handler.ServeHTTP(activateRec, activateReq)
	require.Equal(t, http.StatusOK, activateRec.Code)

	type waitResult struct {
		request  *Request
		timedOut bool
	}
	waitCh := make(chan waitResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, timedOut := holdMgr.WaitForCommand(ctx, authCtx.APIKeyHash, taskID)
		waitCh <- waitResult{request: req, timedOut: timedOut}
	}()

	time.Sleep(100 * time.Millisecond)

	createBody := bytes.NewBufferString(`{"content":"ship fix now","task_id":"task-auto"}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/requests", createBody)
	createReq.Header.Set("Authorization", authHeader)
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusOK, createRec.Code)

	var createResp map[string]any
	require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &createResp))
	requestPayload, ok := createResp["request"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, StatusConsumed, requestPayload["status"])
	require.Equal(t, "task-auto", requestPayload["task_id"])

	result := <-waitCh
	require.False(t, result.timedOut)
	require.NotNil(t, result.request)
	require.Equal(t, "ship fix now", result.request.Content)
	require.Equal(t, "task-auto", result.request.TaskID)

	require.False(t, holdMgr.IsHoldActive(authCtx.APIKeyHash, taskID))

	pending, consumed, _, err := svc.ListRequests(context.Background(), authCtx, taskID, false, "", 10)
	require.NoError(t, err)
	require.Len(t, pending, 0)
	require.Len(t, consumed, 1)
	require.Equal(t, "ship fix now", consumed[0].Content)
}

// TestHoldHTTPCreateRequestDifferentTaskKeepsOriginalHold verifies that hold is task-scoped.
// A directive created for a different task must not release or satisfy a hold on the original task.
func TestHoldHTTPCreateRequestDifferentTaskKeepsOriginalHold(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	holdMgr := NewHoldManager(svc, log.Logger.Named("test_hold"), nil)
	handler := NewCombinedHTTPHandler(svc, holdMgr, log.Logger.Named("test_http"), nil)

	authHeader := "Bearer sk-test-hold-task-scope-123456"
	authCtx, err := askuser.ParseAuthorizationContext(authHeader)
	require.NoError(t, err)

	holdTaskID := "task-a"
	otherTaskID := "task-b"

	activateReq := httptest.NewRequest(http.MethodPost, "/api/hold?task_id="+holdTaskID, nil)
	activateReq.Header.Set("Authorization", authHeader)
	activateRec := httptest.NewRecorder()
	handler.ServeHTTP(activateRec, activateReq)
	require.Equal(t, http.StatusOK, activateRec.Code)

	waitCh := make(chan struct{}, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		_, _ = holdMgr.WaitForCommand(ctx, authCtx.APIKeyHash, holdTaskID)
		waitCh <- struct{}{}
	}()

	time.Sleep(100 * time.Millisecond)

	createBody := bytes.NewBufferString(`{"content":"command for another task","task_id":"task-b"}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/requests", createBody)
	createReq.Header.Set("Authorization", authHeader)
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusOK, createRec.Code)

	var createResp map[string]any
	require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &createResp))
	requestPayload, ok := createResp["request"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, StatusPending, requestPayload["status"])
	require.Equal(t, otherTaskID, requestPayload["task_id"])

	<-waitCh
	require.True(t, holdMgr.IsHoldActive(authCtx.APIKeyHash, holdTaskID))

	pendingOtherTask, _, _, err := svc.ListRequests(context.Background(), authCtx, otherTaskID, false, "", 10)
	require.NoError(t, err)
	require.Len(t, pendingOtherTask, 1)
	require.Equal(t, "command for another task", pendingOtherTask[0].Content)

	holdMgr.ReleaseHold(authCtx.APIKeyHash, holdTaskID)
}
