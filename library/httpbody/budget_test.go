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
	result, err := Enforce(httptest.NewRecorder(), request, Budget{MaxBytes: 1, MinBytesPerSecond: 1, Grace: time.Millisecond})
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
		result, err := Enforce(w, r, Budget{MaxBytes: 1 << 20, MinBytesPerSecond: 1024, Grace: budget})
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
		result, _ := Enforce(unwrappingWriter{w}, r, Budget{MinBytesPerSecond: 1024, Grace: time.Second})
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

// readOutcome starts a server whose handler reads the whole body under budget
// and reports how long the read took and whether it failed on the deadline.
func readOutcome(t *testing.T, budget Budget) (*httptest.Server, <-chan string) {
	t.Helper()
	outcomes := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := Enforce(w, r, budget); err != nil {
			outcomes <- "rejected"
			return
		}
		_, err := io.Copy(io.Discard, r.Body)
		switch {
		case err == nil:
			outcomes <- "complete"
		case IsTimeout(err):
			outcomes <- "cut"
		default:
			outcomes <- "error: " + err.Error()
		}
	}))
	t.Cleanup(server.Close)
	return server, outcomes
}

// sendPaced writes total bytes to the server in chunks spaced by interval.
func sendPaced(t *testing.T, server *httptest.Server, total, chunk int, interval time.Duration) {
	t.Helper()
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: test\r\nContent-Length: %d\r\n\r\n", total)
	require.NoError(t, err)
	go func() {
		buffer := []byte(strings.Repeat("x", chunk))
		for sent := 0; sent < total; sent += chunk {
			if _, err := conn.Write(buffer); err != nil {
				return
			}
			time.Sleep(interval)
		}
	}()
}

func TestMinDataRateAdmitsSlowButLiveBodies(t *testing.T) {
	// 6 KiB at about 4 KiB/s takes ~1.5 s; a flat deadline of the 200ms grace would cut it.
	server, outcomes := readOutcome(t, Budget{MaxBytes: 1 << 20, MinBytesPerSecond: 1024, Grace: 200 * time.Millisecond})
	sendPaced(t, server, 6<<10, 512, 125*time.Millisecond)
	select {
	case outcome := <-outcomes:
		require.Equal(t, "complete", outcome)
	case <-time.After(5 * time.Second):
		t.Fatal("no outcome")
	}
}

func TestMinDataRateCutsTrickleBelowTheRate(t *testing.T) {
	// 8 bytes every 200ms (40 B/s) is far below 1 KiB/s: cut soon after the grace.
	server, outcomes := readOutcome(t, Budget{MaxBytes: 1 << 20, MinBytesPerSecond: 1024, Grace: 200 * time.Millisecond})
	started := time.Now()
	sendPaced(t, server, 4096, 8, 200*time.Millisecond)
	select {
	case outcome := <-outcomes:
		require.Equal(t, "cut", outcome)
		require.Less(t, time.Since(started), 2*time.Second)
	case <-time.After(5 * time.Second):
		t.Fatal("a trickling body was not cut")
	}
}

func TestAbsoluteReadCapBoundsFastEnoughClients(t *testing.T) {
	// The client stays above the minimum rate, but the whole read is capped at 600ms.
	server, outcomes := readOutcome(t, Budget{MaxBytes: 1 << 20, MinBytesPerSecond: 1, Grace: 100 * time.Millisecond, MaxDuration: 600 * time.Millisecond})
	started := time.Now()
	sendPaced(t, server, 64<<10, 1024, 100*time.Millisecond)
	select {
	case outcome := <-outcomes:
		require.Equal(t, "cut", outcome)
		require.Less(t, time.Since(started), 2*time.Second)
		require.GreaterOrEqual(t, time.Since(started), 500*time.Millisecond)
	case <-time.After(5 * time.Second):
		t.Fatal("the absolute cap did not end the read")
	}
	require.Equal(t, 50*time.Millisecond+2*time.Second, Budget{MaxBytes: 2048, MinBytesPerSecond: 1024, Grace: 50 * time.Millisecond}.absoluteLimit(),
		"without MaxDuration the cap is the time a client at exactly the minimum rate needs")
}
