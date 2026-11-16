package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/library/httpbody"
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
// held request has most of the verified in-flight budget charged, a declared
// or chunked verified body that outgrows the rest is refused 503 while it is
// read; anonymous discovery draws on its own budget and is still served. Once
// the held request returns, its charge is released and the same requests
// succeed.
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
	t.Run("anonymous discovery draws on its own budget", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, inflightAnonymousInitialize(t))
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
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

	require.Equal(t, DefaultMCPAnonymousInflightBodyFactor*DefaultMCPAnonymousMaxBodyBytes, RequestBodyBudget{}.normalized().AnonymousMaxInflightBytes)
	require.Equal(t, int64(8<<20), DefaultRequestBodyBudget().AnonymousMaxInflightBytes)
	require.Equal(t, int64(512<<10), RequestBodyBudget{AnonymousMaxBytes: 64 << 10}.normalized().AnonymousMaxInflightBytes,
		"unset follows the anonymous cap")
	require.Equal(t, int64(64<<10), RequestBodyBudget{AnonymousMaxBytes: 64 << 10, AnonymousMaxInflightBytes: 1}.normalized().AnonymousMaxInflightBytes,
		"an anonymous budget below the anonymous cap is raised to it")

	budgets := newIngressBodyBudgets(DefaultRequestBodyBudget().normalized())
	require.Equal(t, int64(128<<20), budgets.verified.Capacity())
	require.Equal(t, int64(32<<20), budgets.verified.TenantShare(), "one key may hold a quarter of the verified budget")
	require.Equal(t, int64(8<<20), budgets.anonymous.Capacity())
	require.Zero(t, budgets.anonymous.TenantShare(), "the anonymous budget has no tenant")
	require.Equal(t, int64(64<<20), newIngressBodyBudgets(RequestBodyBudget{MaxBytes: 1 << 20, MaxInflightBytes: 256 << 20}.normalized()).verified.TenantShare())
	require.Equal(t, int64(4<<20), newIngressBodyBudgets(RequestBodyBudget{MaxBytes: 4 << 20, MaxInflightBytes: 4 << 20}.normalized()).verified.TenantShare(),
		"a share is never below the per-request cap")
	require.GreaterOrEqual(t, maxPendingBodyBytes, int64(httpLogBodyLimit+1), "the logger's preview must fit before classification")
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

// stalledBody is a request body the test feeds by hand and then leaves open.
type stalledBody struct {
	sink     *io.PipeWriter
	finished chan int
}

// startStalled serves request (whose body becomes a pipe) in the background
// and feeds it prefix, returning once the server read those bytes; the body
// then stays open until the test closes it.
func startStalled(t *testing.T, handler http.Handler, request *http.Request, prefix []byte) *stalledBody {
	t.Helper()
	source, sink := io.Pipe()
	request.Body = source
	stalled := &stalledBody{sink: sink, finished: make(chan int, 1)}
	go func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		stalled.finished <- response.Code
	}()
	written := make(chan error, 1)
	go func() {
		_, err := sink.Write(prefix)
		written <- err
	}()
	select {
	case err := <-written:
		require.NoError(t, err)
	case code := <-stalled.finished:
		t.Fatalf("a stalled request was answered %d before its first bytes were read", code)
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled request never read its first bytes")
	}
	return stalled
}

// end closes the stalled body and waits for its request to finish.
func (b *stalledBody) end(t *testing.T) int {
	t.Helper()
	_ = b.sink.CloseWithError(io.ErrUnexpectedEOF)
	select {
	case code := <-b.finished:
		return code
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled request did not end after its sender gave up")
		return 0
	}
}

// anonymousPost is a legacy anonymous POST declaring length bytes.
func anonymousPost(length int64) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/mcp/", http.NoBody)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set(mcpgo.HeaderProtocolVersion, mcpgo.ProtocolVersion20250618)
	request.ContentLength = length
	return request
}

// smallVerifiedToolsList is a small modern tools/list of key.
func smallVerifiedToolsList(t *testing.T, key string) *http.Request {
	t.Helper()
	return modernPost([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{"_meta":`+
		string(mustJSON(t, modernMeta()))+`}}`), "tools/list", "Bearer "+key)
}

// TestIngressAnonymousBodiesCannotBlockVerifiedCallers reproduces the
// independent review of the single in-flight budget, which was charged
// before authentication: credential-less clients trickling bodies just above
// the minimum data rate kept the whole budget full, and verified callers got
// 503. Anonymous bodies now draw on their own budget, so four anonymous
// bodies each holding the anonymous cap minus one byte (filling what was the
// shared budget) leave a verified POST unaffected, while further anonymous
// bodies are refused by the anonymous budget alone.
func TestIngressAnonymousBodiesCannotBlockVerifiedCallers(t *testing.T) {
	const anonymousCap = 16 << 10
	s, _ := newCancelProbeServer(t, WithRequestBodyBudget(RequestBodyBudget{
		MaxBytes: anonymousCap, MaxInflightBytes: 4 * anonymousCap,
		AnonymousMaxBytes: anonymousCap, AnonymousMaxInflightBytes: 4 * anonymousCap,
	}))
	handler := s.Handler()

	var stalled []*stalledBody
	for range 4 {
		start := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"`)
		prefix := append(start, bytes.Repeat([]byte(" "), anonymousCap-1-len(start))...)
		stalled = append(stalled, startStalled(t, handler, anonymousPost(anonymousCap), prefix))
	}

	refused := inflightRefusals(t, inflightEntryAnonymous, httpbody.InflightRefusedCapacity)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, smallVerifiedToolsList(t, cancelVictimKey))
	require.Equal(t, http.StatusOK, response.Code, "anonymous bodies must not block a verified caller: %s", response.Body.String())

	extra := anonymousPost(-1)
	extra.Body = io.NopCloser(bytes.NewReader(bytes.Repeat([]byte(" "), 8<<10)))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, extra)
	requireTransportBusy(t, response)
	require.InDelta(t, refused+1, inflightRefusals(t, inflightEntryAnonymous, httpbody.InflightRefusedCapacity), 0,
		"the anonymous budget, not the verified one, refused it")

	for _, body := range stalled {
		require.Equal(t, http.StatusBadRequest, body.end(t))
	}
}

// TestIngressOneKeyCannotStarveAnotherKey pins the per-key share of the
// verified budget: one key holding its share is refused more (503, counted as
// tenant_share) while another key is still served, and the first key is
// served again once its bodies are released.
func TestIngressOneKeyCannotStarveAnotherKey(t *testing.T) {
	const perRequest = 16 << 10
	s, _ := newCancelProbeServer(t, WithRequestBodyBudget(RequestBodyBudget{MaxBytes: perRequest, MaxInflightBytes: 4 * perRequest}))
	handler := s.Handler()

	held := modernPost(nil, "tools/list", "Bearer "+cancelAttackerKey)
	held.ContentLength = perRequest
	hog := startStalled(t, handler, held, append([]byte(`{"jsonrpc":"2.0"`), bytes.Repeat([]byte(" "), 12<<10)...))

	before := inflightRefusals(t, inflightEntryVerified, httpbody.InflightRefusedTenantShare)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, inflightToolsList(t, false))
	requireTransportBusy(t, response)
	require.InDelta(t, before+1, inflightRefusals(t, inflightEntryVerified, httpbody.InflightRefusedTenantShare), 0,
		"the key's share, not the capacity, refused it")

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, smallVerifiedToolsList(t, cancelVictimKey))
	require.Equal(t, http.StatusOK, response.Code, "another key is not starved: %s", response.Body.String())

	require.Equal(t, http.StatusBadRequest, hog.end(t))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, inflightToolsList(t, false))
	require.Equal(t, http.StatusOK, response.Code, "the key is served again once its bodies are released: %s", response.Body.String())
}

// TestIngressUnverifiedBodiesAreNeverCharged pins classification at the
// gate: a request whose bearer is rejected reads only the logger's preview,
// is assigned to no budget, and leaves both budgets untouched; a verified one
// is charged to its key.
func TestIngressUnverifiedBodiesAreNeverCharged(t *testing.T) {
	fixture := newOneAPITokenFixture(t, issuedIngressKey)
	type observation struct {
		pending, charged, verified, anonymous int64
	}
	var observed observation
	var budgets *ingressBodyBudgets
	gate := withBearerAuthentication(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusTeapot)
	}), func() *mcpauth.Authenticator { return fixture.authenticator(t) }, DefaultMCPAnonymousMaxBodyBytes, "", nil)
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		charge, ok := httpbody.ChargeFromContext(r.Context())
		require.True(t, ok)
		budgets, ok = r.Context().Value(ingressBodyBudgetsKey{}).(*ingressBodyBudgets)
		require.True(t, ok)
		withHTTPLogging(gate, nil).ServeHTTP(w, r)
		observed = observation{charge.Pending(), charge.Charged(), budgets.verified.InUse(), budgets.anonymous.InUse()}
	})
	handler := withRequestBodyBudget(probe, DefaultRequestBodyBudget(), nil)
	body := bytes.Repeat([]byte(" "), 100<<10)

	for _, authorization := range []string{"Bearer sk-not-issued", "Bearer " + strings.Repeat("k", maxAuthorizationHeaderBytes)} {
		request := httptest.NewRequest(http.MethodPost, "/mcp/", bytes.NewReader(body))
		request.Header.Set("Authorization", authorization)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		require.Equal(t, http.StatusUnauthorized, response.Code)
		require.Zero(t, observed.charged, "a rejected bearer is never assigned")
		require.Zero(t, observed.verified)
		require.Zero(t, observed.anonymous)
		require.LessOrEqual(t, observed.pending, maxPendingBodyBytes)
	}

	request := httptest.NewRequest(http.MethodPost, "/mcp/", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+issuedIngressKey)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusTeapot, response.Code)
	require.Equal(t, int64(len(body)), observed.charged, "a verified body is charged every byte it delivered")
	require.Equal(t, int64(len(body)), observed.verified)
	require.Zero(t, observed.anonymous)
	require.Zero(t, budgets.verified.InUse(), "the charge is released when the request ends")
}

// inflightRefusals reads http_request_body_inflight_refused_total{entry,
// reason} from the default registry the API server exports at /metrics.
func inflightRefusals(t *testing.T, entry, reason string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "http_request_body_inflight_refused_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["entry"] == entry && labels["reason"] == reason {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}
