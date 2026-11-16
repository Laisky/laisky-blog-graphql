package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/Laisky/zap/zaptest/observer"
	"github.com/stretchr/testify/require"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	"github.com/Laisky/laisky-blog-graphql/library/httpbody"
)

// observedLogger returns a debug logger whose entries the test can inspect.
func observedLogger(t *testing.T, name string) (glog.Logger, *observer.ObservedLogs) {
	t.Helper()
	core, observed := observer.New(zap.DebugLevel)
	logger, err := glog.NewWithName(name, glog.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
	require.NoError(t, err)
	return logger, observed
}

// errorEntries returns the ERROR (and above) entries observed.
func errorEntries(observed *observer.ObservedLogs) []observer.LoggedEntry {
	var entries []observer.LoggedEntry
	for _, entry := range observed.All() {
		if entry.Level >= zapcore.ErrorLevel {
			entries = append(entries, entry)
		}
	}
	return entries
}

// TestIngressAbortedUploadsLogNoErrors reproduces review finding (2): a
// client that aborts mid-body made the request logger write one ERROR per
// request (unexpected EOF), so a client-side flood became an ERROR flood.
// Client-caused body read failures are DEBUG; 20 aborted uploads over real
// sockets, anonymous and verified, write no ERROR line.
func TestIngressAbortedUploadsLogNoErrors(t *testing.T) {
	logger, observed := observedLogger(t, "aborted-uploads")
	fixture := newOneAPITokenFixture(t, issuedIngressKey)
	s, err := NewServer(nil, nil, nil, nil, rag.Settings{}, nil, nil, nil, nil, ToolsSettings{MCPPipeEnabled: true}, logger,
		WithAuthenticator(fixture.authenticator(t)))
	require.NoError(t, err)
	server := httptest.NewServer(s.Handler())
	t.Cleanup(server.Close)

	for i := range 20 {
		conn, err := net.Dial("tcp", server.Listener.Addr().String())
		require.NoError(t, err)
		authorization := ""
		if i%2 == 1 {
			authorization = "Authorization: Bearer " + issuedIngressKey + "\r\n"
		}
		_, err = fmt.Fprintf(conn, "POST /mcp/ HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\n"+
			"Accept: application/json, text/event-stream\r\n%sContent-Length: 65536\r\n\r\n{\"jsonrpc\":\"2.0\",",
			server.Listener.Addr(), authorization)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)
		require.NoError(t, conn.Close())
	}
	require.Eventually(t, func() bool {
		return len(observed.FilterMessage("request body read ended by the client").All()) >= 20
	}, 5*time.Second, 20*time.Millisecond, "every aborted upload is still logged at DEBUG")
	require.Empty(t, errorEntries(observed), "a client aborting its upload is not a server error")
}

// TestClientBodyReadErrorClassification pins which body read failures the
// request logger treats as caused by the client (DEBUG) and which stay ERROR.
func TestClientBodyReadErrorClassification(t *testing.T) {
	for name, err := range map[string]error{
		"unexpected eof":      io.ErrUnexpectedEOF,
		"wrapped eof":         fmt.Errorf("read body: %w", io.ErrUnexpectedEOF),
		"connection reset":    &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET},
		"broken pipe":         &net.OpError{Op: "read", Net: "tcp", Err: syscall.EPIPE},
		"closed connection":   net.ErrClosed,
		"read timeout":        &net.OpError{Op: "read", Net: "tcp", Err: timeoutError{}},
		"deadline":            fmt.Errorf("read: %w", os.ErrDeadlineExceeded),
		"body too large":      &http.MaxBytesError{Limit: 1},
		"budget spent":        &httpbody.InflightExhaustedError{Capacity: 1},
		"read after close":    http.ErrBodyReadAfterClose,
		"client disconnected": fmt.Errorf("copy: %w", errClientGone()),
	} {
		t.Run("client "+name, func(t *testing.T) {
			require.True(t, isClientBodyReadError(err), "%v", err)
		})
	}
	for name, err := range map[string]error{
		"nil":          nil,
		"plain":        errors.New("disk exploded"),
		"invalid read": io.ErrNoProgress,
	} {
		t.Run("server "+name, func(t *testing.T) {
			require.False(t, isClientBodyReadError(err), "%v", err)
		})
	}

	logger, observed := observedLogger(t, "body-read-classification")
	handler := withHTTPLogging(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), logger)
	for _, failure := range []error{io.ErrUnexpectedEOF, errors.New("disk exploded")} {
		request := httptest.NewRequest(http.MethodPost, "/mcp/", io.MultiReader(strings.NewReader("{"), errorReader{failure}))
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
	require.Len(t, errorEntries(observed), 1, "only the server-side failure stays an ERROR")
}

// timeoutError is a net.Error that timed out.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// errClientGone is the connection-aborted error a peer that vanished causes.
func errClientGone() error {
	return &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNABORTED}
}

// errorReader fails every read with err.
type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

// TestIngressOversizedAuthorizationHeaderIsRefusedCheaply reproduces review
// finding (3): the gate split a raw Authorization header into fields before
// any length bound, so a 1 MiB header (Go's default MaxHeaderBytes) of short
// fields cost about nine times its size in allocations per request. A header
// above mcpauth's bound is now answered 401 invalid_token at once, without
// consulting the authority, and allocates less than twice its length.
func TestIngressOversizedAuthorizationHeaderIsRefusedCheaply(t *testing.T) {
	var calls int
	authenticator, err := mcpauth.NewAuthenticator(mcpauth.ValidatorFunc(func(_ context.Context, _ string) error {
		calls++
		return nil
	}), mcpauth.UnmeteredOptions("header-bound"))
	require.NoError(t, err)
	gate := withBearerAuthentication(http.NotFoundHandler(), func() *mcpauth.Authenticator { return authenticator },
		DefaultMCPAnonymousMaxBodyBytes, "", nil)

	header := "Bearer " + strings.Repeat("a ", 512<<10)
	request := httptest.NewRequest(http.MethodPost, "/mcp/", nil)
	request.Header.Set("Authorization", header)
	response := httptest.NewRecorder()
	gate.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
	require.Contains(t, response.Header().Get("WWW-Authenticate"), `error="invalid_token"`)
	require.Zero(t, calls, "an oversized header never reaches the authority")

	const rounds = 20
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for range rounds {
		gate.ServeHTTP(httptest.NewRecorder(), request)
	}
	runtime.ReadMemStats(&after)
	perRequest := (after.TotalAlloc - before.TotalAlloc) / rounds
	require.Less(t, perRequest, uint64(2*len(header)), "an oversized header must be refused before it is split")
}

// TestBearerHeaderBoundMirrorsAuthenticator keeps the transport's header
// bound equal to mcpauth's: a header at the bound reaches the authenticator,
// which accepts it, and one byte more is refused by both.
func TestBearerHeaderBoundMirrorsAuthenticator(t *testing.T) {
	authenticator, err := mcpauth.NewAuthenticator(mcpauth.InsecureAcceptAllValidator{}, mcpauth.UnmeteredOptions("header-bound"))
	require.NoError(t, err)
	prefix := "Bearer sk-header-bound "
	atBound := prefix + strings.Repeat("x", maxAuthorizationHeaderBytes-len(prefix))
	require.Len(t, atBound, maxAuthorizationHeaderBytes)
	_, err = authenticator.Authenticate(context.Background(), atBound)
	require.NoError(t, err, "mcpauth accepts a header exactly at the transport's bound")
	_, err = authenticator.Authenticate(context.Background(), atBound+"x")
	require.ErrorIs(t, err, mcpauth.ErrInvalidAuthorization, "mcpauth refuses one byte more, like the transport")

	gate := withBearerAuthentication(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }),
		func() *mcpauth.Authenticator { return authenticator }, DefaultMCPAnonymousMaxBodyBytes, "", nil)
	for header, status := range map[string]int{atBound: http.StatusTeapot, atBound + "x": http.StatusUnauthorized} {
		request := httptest.NewRequest(http.MethodGet, "/mcp/", nil)
		request.Header.Set("Authorization", header)
		response := httptest.NewRecorder()
		gate.ServeHTTP(response, request)
		require.Equal(t, status, response.Code, "header length %d", len(header))
	}
}
