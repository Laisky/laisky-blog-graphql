package web

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestServeHTTPDrainsAcceptedRequest verifies the isolated logging lifecycle contract.
func TestServeHTTPDrainsAcceptedRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	accepted, release := make(chan struct{}), make(chan struct{})
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(accepted)
		<-release
		_, _ = io.WriteString(w, "completed")
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveHTTP(ctx, server, listener) }()
	response := make(chan string, 1)
	go func() {
		client := &http.Client{Timeout: 2 * time.Second}
		r, requestErr := client.Get("http://" + listener.Addr().String())
		if requestErr != nil {
			response <- "request failed"
			return
		}
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)
		response <- string(body)
	}()
	<-accepted
	cancel()
	select {
	case err := <-done:
		t.Fatalf("returned before accepted request completed: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	require.Equal(t, "completed", <-response)
	require.NoError(t, <-done)
}

// TestServeHTTPReportsListenerFailure verifies the isolated logging lifecycle contract.
func TestServeHTTPReportsListenerFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	require.NoError(t, listener.Close())
	require.Error(t, serveHTTP(context.Background(), &http.Server{ReadHeaderTimeout: time.Second}, listener))
}
