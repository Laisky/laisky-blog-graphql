package diagnostics

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestLogErrorMalformedHTTPFirstLine exercises net/http's actual raw-line error boundary.
func TestLogErrorMalformedHTTPFirstLine(t *testing.T) {
	testLogErrorMalformedHTTPFirstLine(t, "private-upstream-firstline-canary", "malformed HTTP response")
}

func TestLogErrorMalformedHTTPStatusAndVersion(t *testing.T) {
	for _, test := range []struct {
		name     string
		response string
		prefix   string
	}{
		{"status", "HTTP/1.1 private-upstream-status-canary OK", "malformed HTTP status code"},
		{"version", "HTTP/private-upstream-version-canary 200 OK", "malformed HTTP version"},
	} {
		t.Run(test.name, func(t *testing.T) {
			testLogErrorMalformedHTTPFirstLine(t, test.response, test.prefix)
		})
	}
}

func testLogErrorMalformedHTTPFirstLine(t *testing.T, response, prefix string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			done <- acceptErr
			return
		}
		defer func() { _ = connection.Close() }()
		_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
		_, readErr := http.ReadRequest(bufio.NewReader(connection))
		if readErr != nil {
			done <- readErr
			return
		}
		_, writeErr := io.WriteString(connection, response+"\r\n\r\n")
		done <- writeErr
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+listener.Addr().String()+"/fixture", nil)
	require.NoError(t, err)
	client := &http.Client{Timeout: 3 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	_, err = client.Do(request)
	require.Error(t, err)
	require.Contains(t, err.Error(), prefix)
	select {
	case serverErr := <-done:
		require.NoError(t, serverErr)
	case <-ctx.Done():
		t.Fatal("synthetic server did not finish")
	}
	original := Wrap(err, transportStage)
	text := LogError(original).Error()
	require.Contains(t, text, prefix)
	require.Contains(t, text, "[redacted response line]")
	require.NotContains(t, text, "canary")
	require.Equal(t, unknownValue, Summary(original).Category, "parser message is not classification evidence")
}
