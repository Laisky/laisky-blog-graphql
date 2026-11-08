package httpbody

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEnforceRejectsDeclaredOversizeWithoutReading(t *testing.T) {
	reader := strings.NewReader(strings.Repeat("x", 11))
	request := httptest.NewRequest(http.MethodPost, "/", reader)
	result, err := Enforce(httptest.NewRecorder(), request, Budget{MaxBytes: 10})
	require.True(t, IsTooLarge(err))
	require.Equal(t, int64(10), Limit(err))
	require.False(t, result.DeadlineArmed)
	require.Equal(t, 11, reader.Len(), "nothing may be read from an over-budget body")
}

func TestEnforceCapsStreamedBodies(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 11)))
	request.ContentLength = -1
	_, err := Enforce(httptest.NewRecorder(), request, Budget{MaxBytes: 10})
	require.NoError(t, err)
	_, err = io.ReadAll(request.Body)
	require.True(t, IsTooLarge(err))
	require.Equal(t, int64(10), Limit(err))
}

func TestEnforceLeavesBodylessRequestsAlone(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	result, err := Enforce(httptest.NewRecorder(), request, Budget{MaxBytes: 1, ReadTimeout: time.Millisecond})
	require.NoError(t, err)
	require.False(t, result.DeadlineArmed, "a request without a body must never get a read deadline")
	require.Equal(t, http.NoBody, request.Body)
}

func TestTightenLowersTheCap(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345"))
	require.True(t, IsTooLarge(Tighten(httptest.NewRecorder(), request, 4)))
	request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345"))
	request.ContentLength = -1
	require.NoError(t, Tighten(httptest.NewRecorder(), request, 4))
	_, err := io.ReadAll(request.Body)
	require.True(t, IsTooLarge(err))
}

// TestReadDeadlineCutsSlowBodyButNotTheResponse runs on a real connection:
// a stalled body fails at the budget, while a handler that already consumed
// its body keeps a live request context far beyond the same budget.
func TestReadDeadlineCutsSlowBodyButNotTheResponse(t *testing.T) {
	const budget = 200 * time.Millisecond
	outcomes := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := Enforce(w, r, Budget{MaxBytes: 1 << 20, ReadTimeout: budget})
		if err != nil || !result.DeadlineArmed {
			outcomes <- fmt.Sprintf("not armed: %v %v", err, result.DeadlineErr)
			return
		}
		if _, err := io.ReadAll(r.Body); err != nil {
			if IsTimeout(err) {
				outcomes <- "body timeout"
			} else {
				outcomes <- "body error: " + err.Error()
			}
			return
		}
		select {
		case <-r.Context().Done():
			outcomes <- "context cancelled"
		case <-time.After(4 * budget):
			outcomes <- "context alive"
		}
		_, _ = io.WriteString(w, "done")
	}))
	t.Cleanup(server.Close)

	response, err := server.Client().Post(server.URL, "text/plain", strings.NewReader("complete body"))
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, response.Body)
	require.NoError(t, response.Body.Close())
	require.Equal(t, "context alive", <-outcomes, "the deadline must be released once the body is consumed")

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 100\r\n\r\npartial")
	require.NoError(t, err)
	started := time.Now()
	select {
	case outcome := <-outcomes:
		require.Equal(t, "body timeout", outcome)
		require.Less(t, time.Since(started), budget+time.Second)
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled body was not cut by the read budget")
	}
	_, _ = bufio.NewReader(conn).ReadString('\n')
}

// TestReadDeadlineReachesThroughUnwrappingWriters proves the deadline works
// through wrappers that expose Unwrap, as gin's writer does.
func TestReadDeadlineReachesThroughUnwrappingWriters(t *testing.T) {
	armed := make(chan bool, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, _ := Enforce(unwrappingWriter{w}, r, Budget{ReadTimeout: time.Second})
		armed <- result.DeadlineArmed
	}))
	t.Cleanup(server.Close)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, strings.NewReader("x"))
	require.NoError(t, err)
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.True(t, <-armed)
}

// unwrappingWriter is a minimal middleware writer that supports Unwrap.
type unwrappingWriter struct {
	http.ResponseWriter
}

// Unwrap exposes the wrapped writer to http.ResponseController.
func (w unwrappingWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
