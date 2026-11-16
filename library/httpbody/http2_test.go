package httpbody

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestReadDeadlineCutsSlowBodyOverHTTP2 covers HTTP/2 over TLS (maintainer
// review of PR #95, P3): the read deadline must arm per stream there too, cut
// a stalled body within the budget, and leave a consumed request's context
// alive. HTTP/1.1 is covered by TestReadDeadlineCutsSlowBodyButNotTheResponse.
func TestReadDeadlineCutsSlowBodyOverHTTP2(t *testing.T) {
	const budget = 200 * time.Millisecond
	outcomes := make(chan string, 2)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			outcomes <- "protocol " + r.Proto
			return
		}
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
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	client := server.Client()

	response, err := client.Post(server.URL, "text/plain", strings.NewReader("complete body"))
	require.NoError(t, err)
	require.Equal(t, 2, response.ProtoMajor)
	_, _ = io.Copy(io.Discard, response.Body)
	require.NoError(t, response.Body.Close())
	require.Equal(t, "context alive", <-outcomes, "the deadline must be released once the body is consumed")

	// A body that sends a few bytes and then stalls on an open stream.
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, reader)
	require.NoError(t, err)
	request.ContentLength = 100
	go func() {
		_, _ = writer.Write([]byte("partial"))
		<-ctx.Done()
	}()
	go func() {
		if resp, err := client.Do(request); err == nil {
			_ = resp.Body.Close()
		}
	}()
	started := time.Now()
	select {
	case outcome := <-outcomes:
		require.Equal(t, "body timeout", outcome)
		require.Less(t, time.Since(started), budget+time.Second)
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled HTTP/2 body was not cut by the read budget")
	}
}
