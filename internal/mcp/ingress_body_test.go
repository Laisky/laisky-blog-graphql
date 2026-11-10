package mcp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	glog "github.com/Laisky/go-utils/v6/log"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
)

// countingBody yields size bytes lazily and records how many were consumed,
// so a test can prove a rejected request was not buffered.
type countingBody struct {
	remaining int64
	read      atomic.Int64
}

// Read produces filler bytes until the declared size is exhausted.
func (b *countingBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > b.remaining {
		n = b.remaining
	}
	for i := range p[:n] {
		p[i] = ' '
	}
	b.remaining -= n
	b.read.Add(n)
	return int(n), nil
}

// TestIngressRejectsOversizedBodyBeforeAuthOrBuffering reproduces issue #88:
// a declared body above the MCP budget is refused with 413 before any byte is
// read and before the credential is validated.
func TestIngressRejectsOversizedBodyBeforeAuthOrBuffering(t *testing.T) {
	fixture := newOneAPITokenFixture(t, issuedIngressKey)
	preferences := newUserPreferenceServiceForToolsListTest(t, "file:"+t.Name()+"?mode=memory&cache=shared")
	previous := fixture.calls.Load()
	s, err := NewServer(nil, nil, preferences, nil, rag.Settings{}, nil, nil, nil, nil, ToolsSettings{MCPPipeEnabled: true}, glog.Shared,
		WithAuthenticator(fixture.authenticator(t)))
	require.NoError(t, err)

	for _, authorization := range []string{"", "Bearer " + issuedIngressKey, "Bearer sk-random-oversized"} {
		t.Run(fmt.Sprintf("auth_len_%d", len(authorization)), func(t *testing.T) {
			const limit = int64(32 << 20)
			body := &countingBody{remaining: limit + 1}
			request := httptest.NewRequest(http.MethodPost, "/mcp/", body)
			request.ContentLength = limit + 1
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			if authorization != "" {
				request.Header.Set("Authorization", authorization)
			}
			response := httptest.NewRecorder()
			s.Handler().ServeHTTP(response, request)
			require.Zero(t, body.read.Load(), "an over-budget body must be refused before it is read")
			require.Equal(t, http.StatusRequestEntityTooLarge, response.Code, response.Body.String())
		})
	}
	require.Equal(t, previous, fixture.calls.Load(), "no credential validation may run for an over-budget request")
}

// TestIngressTerminatesSlowBodyWithinReadBudget reproduces the slow-body half
// of issue #88: a client that stalls mid-body must not hold the handler.
func TestIngressTerminatesSlowBodyWithinReadBudget(t *testing.T) {
	const budget = 300 * time.Millisecond
	s, err := NewServer(nil, nil, nil, nil, rag.Settings{}, nil, nil, nil, nil, ToolsSettings{MCPPipeEnabled: true}, glog.Shared,
		WithRequestBodyBudget(RequestBodyBudget{ReadGrace: budget, MinBytesPerSecond: 1024}))
	require.NoError(t, err)
	server := httptest.NewServer(s.Handler())
	t.Cleanup(server.Close)

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = fmt.Fprintf(conn, "POST /mcp/ HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\n"+
		"Accept: application/json, text/event-stream\r\nContent-Length: 4096\r\n\r\n{\"jsonrpc\":", server.Listener.Addr())
	require.NoError(t, err)

	started := time.Now()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, readErr := bufio.NewReader(conn).ReadString('\n')
	elapsed := time.Since(started)
	var netErr net.Error
	if readErr != nil && errorsAsNet(readErr, &netErr) && netErr.Timeout() {
		t.Fatalf("server still waiting for the stalled body after %s", elapsed)
	}
	require.Less(t, elapsed, budget+2*time.Second, "the stalled body must be cut by the read budget")
	require.GreaterOrEqual(t, elapsed, budget-50*time.Millisecond, "the budget, not an unrelated error, must end the request")
}

// errorsAsNet reports whether err is a net.Error.
func errorsAsNet(err error, target *net.Error) bool {
	value, ok := err.(net.Error) //nolint:errorlint // conn reads return net.Error directly
	if ok {
		*target = value
	}
	return ok
}

// TestIngressReadBudgetDoesNotCutHeldRequests guards the streaming contract:
// once the body is consumed, a long-running tool keeps its request context.
func TestIngressReadBudgetDoesNotCutHeldRequests(t *testing.T) {
	// The tool outlives the body read budget fourfold; it must still finish.
	s, err := NewServer(nil, nil, nil, nil, rag.Settings{}, nil, nil, nil, nil, ToolsSettings{MCPPipeEnabled: true}, glog.Shared,
		WithRequestBodyBudget(RequestBodyBudget{ReadGrace: 200 * time.Millisecond, MinBytesPerSecond: 1024}))
	require.NoError(t, err)
	s.billingReporter = nil
	s.toolHandlers["test_session_effect"] = func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		select {
		case <-ctx.Done():
			return mcpgo.NewToolResultText("held request cancelled"), nil
		case <-time.After(800 * time.Millisecond):
			return mcpgo.NewToolResultText("held request survived"), nil
		}
	}
	server := httptest.NewServer(s.Handler())
	t.Cleanup(server.Close)

	request := newModernToolCallRequest(t, context.Background(), server.URL+"/mcp/", "Bearer "+issuedIngressKey)
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode, string(payload))
	require.Contains(t, string(payload), "held request survived")
}
