package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
)

const (
	cancelVictimKey   = "sk-cancel-victim"
	cancelAttackerKey = "sk-cancel-attacker"
)

// cancelProbe is a tool that blocks until its request is canceled or released.
type cancelProbe struct {
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
}

// newCancelProbeServer registers the blocking probe tool on a server that
// accepts the victim's and the attacker's keys.
func newCancelProbeServer(t *testing.T, options ...ServerOption) (*Server, *cancelProbe) {
	t.Helper()
	s, probe, _ := newCancelProbeServerWithFixture(t, options...)
	return s, probe
}

// newCancelProbeServerWithFixture is newCancelProbeServer that also returns
// the one-api fixture, whose call counter shows whether a request was validated.
func newCancelProbeServerWithFixture(t *testing.T, options ...ServerOption) (*Server, *cancelProbe, *oneAPITokenFixture) {
	t.Helper()
	fixture := newOneAPITokenFixture(t, cancelVictimKey, cancelAttackerKey)
	s, _ := newIngressTestServer(t, fixture, options...)
	probe := &cancelProbe{started: make(chan struct{}, 1), canceled: make(chan struct{}, 1), release: make(chan struct{})}
	s.sdk.AddTool(mcpgo.NewTool("test_cancel_probe"), func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		probe.started <- struct{}{}
		select {
		case <-ctx.Done():
			probe.canceled <- struct{}{}
			return mcpgo.NewToolResultError("canceled"), nil
		case <-probe.release:
			return mcpgo.NewToolResultText("probe finished"), nil
		}
	})
	return s, probe, fixture
}

// modernProbeCall builds the victim's modern stateless tools/call with id 7.
func modernProbeCall(t *testing.T, authorization string) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call",
		"params": map[string]any{"name": "test_cancel_probe", "arguments": map[string]any{}, "_meta": modernMeta()}})
	require.NoError(t, err)
	request := modernPost(body, "tools/call", authorization)
	request.Header.Set(mcpgo.HeaderName, "test_cancel_probe")
	return request
}

// modernCancellation builds a modern notifications/cancelled for requestID.
func modernCancellation(t *testing.T, requestID int, authorization string) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled",
		"params": map[string]any{"requestId": requestID, "_meta": modernMeta()}})
	require.NoError(t, err)
	return modernPost(body, "notifications/cancelled", authorization)
}

// modernMeta is the protocol 2026-07-28 request metadata.
func modernMeta() map[string]any {
	return map[string]any{
		mcpgo.MetaKeyProtocolVersion:    mcpgo.ProtocolVersion20260728,
		mcpgo.MetaKeyClientInfo:         map[string]any{"name": "cancel-test", "version": "1"},
		mcpgo.MetaKeyClientCapabilities: map[string]any{},
	}
}

// modernPost wraps body in a protocol 2026-07-28 POST.
func modernPost(body []byte, method, authorization string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/mcp/", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set(mcpgo.HeaderProtocolVersion, mcpgo.ProtocolVersion20260728)
	request.Header.Set(mcpgo.HeaderMethod, method)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	return request
}

// runVictim starts the victim's in-flight call and waits until the tool runs.
func runVictim(t *testing.T, handler http.Handler, probe *cancelProbe, request *http.Request) (*httptest.ResponseRecorder, chan struct{}) {
	t.Helper()
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(response, request)
	}()
	select {
	case <-probe.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the victim's tool never started")
	}
	return response, done
}

// TestIngressCancellationCannotCrossCallers reproduces the independent
// review of this follow-up: the SDK keys in-flight modern requests by request
// id alone, because 2026-07-28 requests have no session, so a POSTed
// notifications/cancelled from anyone aborted any caller's modern request with
// that id. Anonymous callers may no longer send it, and a modern
// notifications/cancelled is accepted without effect for everyone: the
// 2026-07-28 Streamable HTTP transport cancels by closing the response stream.
func TestIngressCancellationCannotCrossCallers(t *testing.T) {
	for name, attacker := range map[string]struct {
		authorization string
		status        int
	}{
		"anonymous":      {authorization: "", status: http.StatusUnauthorized},
		"another tenant": {authorization: "Bearer " + cancelAttackerKey, status: http.StatusAccepted},
		"the same key":   {authorization: "Bearer " + cancelVictimKey, status: http.StatusAccepted},
	} {
		t.Run(name, func(t *testing.T) {
			s, probe := newCancelProbeServer(t)
			handler := s.Handler()
			victim, done := runVictim(t, handler, probe, modernProbeCall(t, "Bearer "+cancelVictimKey))

			attack := httptest.NewRecorder()
			handler.ServeHTTP(attack, modernCancellation(t, 7, attacker.authorization))
			require.Equal(t, attacker.status, attack.Code, attack.Body.String())

			select {
			case <-probe.canceled:
				t.Fatal("a POSTed modern cancellation aborted an in-flight request")
			case <-time.After(150 * time.Millisecond):
			}
			close(probe.release)
			<-done
			require.Equal(t, http.StatusOK, victim.Code, victim.Body.String())
			require.Contains(t, victim.Body.String(), "probe finished")
		})
	}
}

// TestIngressLegacySessionCancellationStillWorks is the positive control:
// a legacy session's notifications/cancelled is scoped to that session's
// credential-bound id and keeps canceling its own in-flight request.
func TestIngressLegacySessionCancellationStillWorks(t *testing.T) {
	s, probe := newCancelProbeServer(t)
	handler := s.Handler()
	session := initializeReviewSession(t, handler, mcpgo.ProtocolVersion20250618, "Bearer "+cancelVictimKey)

	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 11, "method": "tools/call",
		"params": map[string]any{"name": "test_cancel_probe", "arguments": map[string]any{}}})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/mcp/", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	for key, values := range session {
		request.Header[key] = values
	}
	_, done := runVictim(t, handler, probe, request)

	cancel := sendSDKWireRequest(t, handler, map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled",
		"params": map[string]any{"requestId": 11}}, session)
	require.Equal(t, http.StatusAccepted, cancel.Code, cancel.Body.String())
	select {
	case <-probe.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("the session owner's cancellation must still reach its request")
	}
	<-done
}

// TestCancellationGuardIgnoresOnlyModernCancellations pins the guard's
// classification on the verified path without a server: only a modern-era
// notifications/cancelled (including an "id": null one, which the SDK treats
// as a notification) is held back; the body of anything else reaches next.
func TestCancellationGuardIgnoresOnlyModernCancellations(t *testing.T) {
	for name, tc := range map[string]struct {
		body      string
		modern    bool
		forwarded bool
	}{
		"modern cancellation":           {`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`, true, false},
		"modern cancellation with null": {`{"jsonrpc":"2.0","id":null,"method":"notifications/cancelled"}`, true, false},
		"modern case-folded key":        {`{"jsonrpc":"2.0","METHOD":"notifications/cancelled"}`, true, false},
		"modern tool call":              {`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x"}}`, true, true},
		"modern unparseable body":       {`not json`, true, true},
		"legacy cancellation":           {`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`, false, true},
	} {
		t.Run(name, func(t *testing.T) {
			var received []byte
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var err error
				received, err = io.ReadAll(r.Body)
				require.NoError(t, err)
				w.WriteHeader(http.StatusTeapot)
			})
			request := httptest.NewRequest(http.MethodPost, "/mcp/", bytes.NewReader([]byte(tc.body)))
			version := mcpgo.ProtocolVersion20250618
			if tc.modern {
				version = mcpgo.ProtocolVersion20260728
			}
			request.Header.Set(mcpgo.HeaderProtocolVersion, version)
			request = request.WithContext(mcpauth.WithContext(request.Context(), authtest.Verified(cancelVictimKey)))
			response := httptest.NewRecorder()
			serveVerified(response, request, next, nil)
			if tc.forwarded {
				require.Equal(t, http.StatusTeapot, response.Code)
				require.Equal(t, tc.body, string(received), "the full body is replayed")
				return
			}
			require.Equal(t, http.StatusAccepted, response.Code)
			require.Empty(t, response.Body.String())
			require.Nil(t, received)
		})
	}
}

// TestIngressModernRequestIsCanceledByClosingItsStream is the 2026-07-28
// replacement for a POSTed cancellation: a client that closes its request's
// response stream cancels that request, and only that one.
func TestIngressModernRequestIsCanceledByClosingItsStream(t *testing.T) {
	s, probe := newCancelProbeServer(t)
	server := httptest.NewServer(s.Handler())
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	request := modernProbeCall(t, "Bearer "+cancelVictimKey)
	outbound, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/mcp/", request.Body)
	require.NoError(t, err)
	outbound.Header = request.Header.Clone()
	done := make(chan error, 1)
	go func() {
		response, err := server.Client().Do(outbound)
		if err == nil {
			_ = response.Body.Close()
		}
		done <- err
	}()
	select {
	case <-probe.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the request's tool never started")
	}
	cancel()
	select {
	case <-probe.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the stream must cancel the request")
	}
	require.ErrorIs(t, <-done, context.Canceled)
}

// TestModernCancellationPolicyClassifiesLikeTheSDK pins the isolated policy:
// it decodes a body exactly as the SDK's Streamable HTTP transport does, so a
// cancellation cannot hide from it behind an encoding/json quirk, and nothing
// else is mistaken for one.
func TestModernCancellationPolicyClassifiesLikeTheSDK(t *testing.T) {
	padding := strings.Repeat("x", 256<<10)
	for name, tc := range map[string]struct {
		body   string
		cancel bool
	}{
		"plain cancellation":          {`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`, true},
		"case-folded key":             {`{"jsonrpc":"2.0","METHOD":"notifications/cancelled"}`, true},
		"mixed-case key":              {`{"jsonrpc":"2.0","Method":"notifications/cancelled"}`, true},
		"escaped key":                 {`{"jsonrpc":"2.0","\u006dethod":"notifications/cancelled"}`, true},
		"escaped value":               {`{"jsonrpc":"2.0","method":"notifications\/cancelled"}`, true},
		"duplicate key, cancel last":  {`{"jsonrpc":"2.0","id":1,"method":"tools/call","method":"notifications/cancelled"}`, true},
		"duplicate key, cancel first": {`{"jsonrpc":"2.0","id":1,"method":"notifications/cancelled","method":"tools/call"}`, false},
		"method after large params":   {`{"jsonrpc":"2.0","params":{"pad":"` + padding + `"},"method":"notifications/cancelled"}`, true},
		"with a null id":              {`{"jsonrpc":"2.0","id":null,"method":"notifications/cancelled"}`, true},
		"trailing whitespace":         {`{"jsonrpc":"2.0","method":"notifications/cancelled"}` + "  \n", true},
		"other notification":          {`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":1}}`, false},
		"tool call":                   {`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x"}}`, false},
		"batch array":                 {`[{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7}}]`, false},
		"non-string method":           {`{"jsonrpc":"2.0","method":7}`, false},
		"invalid json":                {`{"method":"notifications/cancelled"`, false},
		"empty body":                  {``, false},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.cancel, isModernCancellation([]byte(tc.body)))
		})
	}

	for name, tc := range map[string]struct {
		method   string
		version  string
		classify bool
	}{
		"modern post":       {http.MethodPost, mcpgo.ProtocolVersion20260728, true},
		"legacy post":       {http.MethodPost, mcpgo.ProtocolVersion20250618, false},
		"no version header": {http.MethodPost, "", false},
		"modern get":        {http.MethodGet, mcpgo.ProtocolVersion20260728, false},
		"modern delete":     {http.MethodDelete, mcpgo.ProtocolVersion20260728, false},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, "/mcp/", nil)
			if tc.version != "" {
				request.Header.Set(mcpgo.HeaderProtocolVersion, tc.version)
			}
			require.Equal(t, tc.classify, needsCancellationClassification(request))
		})
	}
}

// TestIngressModernBatchCancellationNeverReachesARequest covers the one shape
// the policy leaves to the SDK: a batch array is not classified, reaches the
// SDK unchanged, and the SDK refuses it as a parse error without canceling
// the victim's in-flight request.
func TestIngressModernBatchCancellationNeverReachesARequest(t *testing.T) {
	s, probe := newCancelProbeServer(t)
	handler := s.Handler()
	victim, done := runVictim(t, handler, probe, modernProbeCall(t, "Bearer "+cancelVictimKey))

	batch := modernPost([]byte(`[{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7}}]`),
		"notifications/cancelled", "Bearer "+cancelAttackerKey)
	attack := httptest.NewRecorder()
	handler.ServeHTTP(attack, batch)
	require.NotEqual(t, http.StatusAccepted, attack.Code, "a batch is not an accepted notification")
	require.Contains(t, attack.Body.String(), "not valid json", "the SDK refuses a batch as a parse error")

	select {
	case <-probe.canceled:
		t.Fatal("a batched modern cancellation aborted an in-flight request")
	case <-time.After(150 * time.Millisecond):
	}
	close(probe.release)
	<-done
	require.Equal(t, http.StatusOK, victim.Code, victim.Body.String())
	require.Contains(t, victim.Body.String(), "probe finished")
}
