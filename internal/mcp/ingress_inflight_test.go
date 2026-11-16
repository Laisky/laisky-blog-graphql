package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

// inflightTestBudget leaves room for one held probe call plus a little, so a
// second body of inflightTestBodyBytes does not fit while the probe runs.
const (
	inflightTestBudget    int64 = 8 << 10
	inflightTestBodyBytes       = 8000
)

// paddedJSON encodes message and pads it with trailing whitespace (valid
// JSON) to exactly size bytes.
func paddedJSON(t *testing.T, message map[string]any, size int) []byte {
	t.Helper()
	body, err := json.Marshal(message)
	require.NoError(t, err)
	require.Less(t, len(body), size)
	return append(body, bytes.Repeat([]byte(" "), size-len(body))...)
}

// inflightToolsList is a padded modern tools/list of the attacker's key;
// chunked drops its declared length so the transport must charge it while
// reading.
func inflightToolsList(t *testing.T, chunked bool) *http.Request {
	t.Helper()
	body := paddedJSON(t, map[string]any{"jsonrpc": "2.0", "id": 9, "method": "tools/list", "params": map[string]any{"_meta": modernMeta()}}, inflightTestBodyBytes)
	request := modernPost(body, "tools/list", "Bearer "+cancelAttackerKey)
	if chunked {
		request.Body = io.NopCloser(bytes.NewReader(body))
		request.ContentLength = -1
	}
	return request
}

// inflightAnonymousInitialize is a padded anonymous discovery request.
func inflightAnonymousInitialize(t *testing.T) *http.Request {
	t.Helper()
	body := paddedJSON(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": mcpgo.ProtocolVersion20250618, "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "inflight-test", "version": "1"},
	}}, inflightTestBodyBytes)
	request := httptest.NewRequest(http.MethodPost, "/mcp/", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set(mcpgo.HeaderProtocolVersion, mcpgo.ProtocolVersion20250618)
	return request
}

// requireTransportBusy asserts the retryable 503 of a spent in-flight budget.
func requireTransportBusy(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	require.Equal(t, strconv.Itoa(transportBusyRetryAfterSeconds), response.Header().Get("Retry-After"))
	require.Equal(t, "close", response.Header().Get("Connection"))
	var payload ingressError
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload), response.Body.String())
	require.Equal(t, "temporarily_unavailable", payload.Error)
	require.Contains(t, payload.Description, "request bodies")
}

// TestIngressInflightBudgetBoundsConcurrentBodies reproduces review finding
// P3-b of PR #95: the per-request cap alone let any number of concurrent MCP
// requests each hold a large body (the SDK io.ReadAll()s every POST, and the
// transport buffers modern and anonymous ones for classification). While a
// held request has most of the transport's in-flight budget charged, a
// declared or chunked verified body that outgrows the rest is refused 503
// while it is read, and anonymous discovery is charged too. Once the held
// request returns, its charge is released and the same requests succeed.
func TestIngressInflightBudgetBoundsConcurrentBodies(t *testing.T) {
	s, probe := newCancelProbeServer(t,
		WithRequestBodyBudget(RequestBodyBudget{MaxBytes: inflightTestBudget, MaxInflightBytes: inflightTestBudget}))
	handler := s.Handler()
	victim, done := runVictim(t, handler, probe, modernProbeCall(t, "Bearer "+cancelVictimKey))

	t.Run("declared body refused once it outgrows the budget", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, inflightToolsList(t, false))
		requireTransportBusy(t, response)
	})
	t.Run("chunked body refused once it outgrows the budget", func(t *testing.T) {
		request := inflightToolsList(t, true)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		requireTransportBusy(t, response)
	})
	t.Run("anonymous discovery is charged too", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, inflightAnonymousInitialize(t))
		requireTransportBusy(t, response)
	})

	close(probe.release)
	<-done
	require.Equal(t, http.StatusOK, victim.Code, victim.Body.String())
	require.Contains(t, victim.Body.String(), "probe finished")

	for _, chunked := range []bool{false, true} {
		t.Run("tools/list fits once the held request returned, chunked="+strconv.FormatBool(chunked), func(t *testing.T) {
			request := inflightToolsList(t, chunked)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.NotContains(t, response.Body.String(), "temporarily_unavailable")
		})
	}
	t.Run("anonymous discovery fits once the held request returned", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, inflightAnonymousInitialize(t))
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), "protocolVersion")
	})
}

// TestIngressInflightBudgetDefaults pins the transport default (four bodies
// at the per-request cap) and the floor that keeps one body at the cap
// admissible on an idle transport.
func TestIngressInflightBudgetDefaults(t *testing.T) {
	require.Equal(t, DefaultMCPInflightBodyFactor*DefaultMCPMaxBodyBytes, RequestBodyBudget{}.normalized().MaxInflightBytes)
	require.Equal(t, DefaultMCPInflightBodyFactor*DefaultMCPMaxBodyBytes, DefaultRequestBodyBudget().MaxInflightBytes)
	require.Equal(t, int64(4<<20), RequestBodyBudget{MaxBytes: 1 << 20}.normalized().MaxInflightBytes, "unset follows the configured cap")
	require.Equal(t, int64(1<<20), RequestBodyBudget{MaxBytes: 1 << 20, MaxInflightBytes: 1024}.normalized().MaxInflightBytes,
		"a budget below the cap is raised to it")
	require.Equal(t, int64(3<<20), RequestBodyBudget{MaxBytes: 1 << 20, MaxInflightBytes: 3 << 20}.normalized().MaxInflightBytes)
}

// TestIngressInflightBudgetIgnoresUnsentDeclaredBytes is the regression of
// the first design, which charged a declared Content-Length up front: the
// budget wrapper runs before authentication, so slow senders that declared
// large bodies but sent only a few bytes held the whole budget while using
// almost no memory, refusing every other MCP POST. Bodies are charged only
// the bytes they delivered, so a fresh small verified POST is still admitted
// while they stall, and the stalled requests end once their senders give up.
func TestIngressInflightBudgetIgnoresUnsentDeclaredBytes(t *testing.T) {
	s, _ := newCancelProbeServer(t,
		WithRequestBodyBudget(RequestBodyBudget{MaxBytes: inflightTestBudget, MaxInflightBytes: inflightTestBudget}))
	handler := s.Handler()

	const stalled = 16
	sinks := make([]*io.PipeWriter, 0, stalled)
	finished := make(chan int, stalled)
	for i := range stalled {
		source, sink := io.Pipe()
		sinks = append(sinks, sink)
		var request *http.Request
		if i%2 == 0 {
			request = httptest.NewRequest(http.MethodPost, "/mcp/", source)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			request.Header.Set(mcpgo.HeaderProtocolVersion, mcpgo.ProtocolVersion20250618)
		} else {
			request = modernPost(nil, "tools/list", "Bearer "+cancelAttackerKey)
			request.Body = source
		}
		request.ContentLength = inflightTestBodyBytes
		go func() {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			finished <- response.Code
		}()
		// io.Pipe returns from Write only once the server read the bytes.
		written := make(chan error, 1)
		go func() {
			_, err := sink.Write([]byte(`{"jsonrpc"`))
			written <- err
		}()
		select {
		case err := <-written:
			require.NoError(t, err)
		case code := <-finished:
			t.Fatalf("stalled request %d was answered %d before its first bytes were read", i, code)
		case <-time.After(5 * time.Second):
			t.Fatalf("stalled request %d never read its first bytes", i)
		}
	}

	for _, chunked := range []bool{false, true} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, inflightToolsList(t, chunked))
		require.Equal(t, http.StatusOK, response.Code, "chunked=%v: %s", chunked, response.Body.String())
	}
	small := modernPost([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{"_meta":`+
		string(mustJSON(t, modernMeta()))+`}}`), "tools/list", "Bearer "+cancelVictimKey)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, small)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	for _, sink := range sinks {
		_ = sink.CloseWithError(io.ErrUnexpectedEOF)
	}
	for range stalled {
		select {
		case code := <-finished:
			require.Equal(t, http.StatusBadRequest, code, "a truncated body is unreadable, not a budget refusal")
		case <-time.After(5 * time.Second):
			t.Fatal("a stalled request did not end after its sender gave up")
		}
	}
}

// mustJSON encodes value for a hand-built request body.
func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}
