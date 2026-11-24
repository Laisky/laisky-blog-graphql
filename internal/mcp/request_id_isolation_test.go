package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

type requestIDProbe struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func TestIngressLegacyRequestIDIsolation(t *testing.T) {
	fixture := newOneAPITokenFixture(t, cancelVictimKey, cancelAttackerKey)
	s, _ := newIngressTestServer(t, fixture)
	probes := map[string]*requestIDProbe{}
	for _, name := range []string{"original", "distinct", "other"} {
		probes[name] = &requestIDProbe{started: make(chan struct{}, 2), release: make(chan struct{})}
	}
	var forbidden atomic.Int32
	s.sdk.AddTool(mcpgo.NewTool("test_id_probe"), func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name := req.GetString("case", "")
		p := probes[name]
		if p == nil {
			forbidden.Add(1)
			return mcpgo.NewToolResultError("unexpected dispatch"), nil
		}
		p.calls.Add(1)
		p.started <- struct{}{}
		select {
		case <-p.release:
			return mcpgo.NewToolResultText(name), nil
		case <-ctx.Done():
			return mcpgo.NewToolResultError("canceled"), nil
		}
	})
	handler := s.Handler()
	alpha := initializeReviewSession(t, handler, mcpgo.ProtocolVersion20250618, "Bearer "+cancelVictimKey)
	beta := initializeReviewSession(t, handler, mcpgo.ProtocolVersion20250618, "Bearer "+cancelAttackerKey)
	build := func(id int, name string, headers http.Header) *http.Request {
		t.Helper()
		body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "tools/call", "id": id, "params": map[string]any{"name": "test_id_probe", "arguments": map[string]any{"case": name}}})
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/mcp/", bytes.NewReader(body))
		req.Header = headers.Clone()
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		return req
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	wait := func(done <-chan struct{}) {
		t.Helper()
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("timed out waiting for MCP handler")
		}
	}
	start := func(req *http.Request) (*httptest.ResponseRecorder, <-chan struct{}) {
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() { defer close(done); handler.ServeHTTP(rec, req.WithContext(ctx)) }()
		return rec, done
	}
	assertResult := func(rec *httptest.ResponseRecorder, name string) {
		t.Helper()
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, decodeMCPResponse(t, rec.Body.Bytes()), "result")
		require.Contains(t, rec.Body.String(), name)
	}
	original, originalDone := start(build(71, "original", alpha))
	wait(probes["original"].started)
	distinct, distinctDone := start(build(72, "distinct", alpha))
	wait(probes["distinct"].started)
	other, otherDone := start(build(71, "other", beta))
	wait(probes["other"].started)
	dup, dupDone := start(build(71, "duplicate", alpha))
	wait(dupDone)
	payload := decodeMCPResponse(t, dup.Body.Bytes())
	require.Contains(t, payload, "error")
	require.NotContains(t, payload, "result")
	mismatched := alpha.Clone()
	mismatched.Set("Authorization", "Bearer "+cancelAttackerKey)
	mismatch, mismatchDone := start(build(73, "mismatch", mismatched))
	wait(mismatchDone)
	require.GreaterOrEqual(t, mismatch.Code, 400)
	require.Less(t, mismatch.Code, 500)
	require.Zero(t, forbidden.Load())
	for _, p := range probes {
		close(p.release)
	}
	wait(originalDone)
	wait(distinctDone)
	wait(otherDone)
	assertResult(original, "original")
	assertResult(distinct, "distinct")
	assertResult(other, "other")
	reused, reusedDone := start(build(71, "original", alpha))
	wait(reusedDone)
	assertResult(reused, "original")
	require.Equal(t, int32(2), probes["original"].calls.Load())
	require.Equal(t, int32(1), probes["distinct"].calls.Load())
	require.Equal(t, int32(1), probes["other"].calls.Load())
}

// Both requests use the ordinary client-generated ID 7. Closing one HTTP
// request must not cancel another caller's request with the same JSON-RPC ID.
func TestIngressModernRequestIDIsolation(t *testing.T) {
	fixture := newOneAPITokenFixture(t, cancelVictimKey, cancelAttackerKey)
	s, _ := newIngressTestServer(t, fixture)
	started := make(chan string, 2)
	canceled := make(chan string, 2)
	release := make(chan struct{})
	s.sdk.AddTool(mcpgo.NewTool("test_id_probe"), func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name := req.GetString("case", "")
		started <- name
		select {
		case <-ctx.Done():
			canceled <- name
			return mcpgo.NewToolResultError("canceled"), nil
		case <-release:
			return mcpgo.NewToolResultText(name), nil
		}
	})
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	ctx, end := context.WithTimeout(t.Context(), 5*time.Second)
	defer end()
	closedCtx, closeRequest := context.WithCancel(ctx)
	defer closeRequest()
	type result struct {
		status int
		body   []byte
		err    error
	}
	begin := func(ctx context.Context, authorization, name string) <-chan result {
		t.Helper()
		body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call", "params": map[string]any{"name": "test_id_probe", "arguments": map[string]any{"case": name}, "_meta": modernMeta()}})
		require.NoError(t, err)
		request := modernPost(body, "tools/call", authorization)
		request.Header.Set(mcpgo.HeaderName, "test_id_probe")
		outbound, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/mcp/", request.Body)
		require.NoError(t, err)
		outbound.Header = request.Header.Clone()
		done := make(chan result, 1)
		go func() {
			resp, err := server.Client().Do(outbound)
			if err != nil {
				done <- result{err: err}
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			done <- result{status: resp.StatusCode, body: body, err: err}
		}()
		return done
	}
	waitName := func(ch <-chan string, name string) {
		t.Helper()
		select {
		case actual := <-ch:
			require.Equal(t, name, actual)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", name)
		}
	}
	first := begin(closedCtx, "Bearer "+cancelVictimKey, "first")
	waitName(started, "first")
	second := begin(ctx, "Bearer "+cancelAttackerKey, "second")
	waitName(started, "second")
	closeRequest()
	waitName(canceled, "first")
	require.ErrorIs(t, (<-first).err, context.Canceled)
	close(release)
	completed := <-second
	require.NoError(t, completed.err)
	require.Equal(t, http.StatusOK, completed.status, string(completed.body))
	require.Contains(t, decodeMCPResponse(t, completed.body), "result")
	require.Contains(t, string(completed.body), "second")
}
