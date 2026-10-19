package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/Laisky/zap/zaptest/observer"
	"github.com/stretchr/testify/require"
)

// ingressProxy runs only the test's own loopback nginx master and workers.
// The production image, no-network container and synthetic upstream are supplied
// by the qualification driver; no system nginx config, PID or listener is used.
func ingressProxy(t *testing.T, backend, limit string) (string, string) {
	t.Helper()
	if os.Getenv("GRAPHQL_TEST_ALERT_INGRESS") != "1" {
		t.Skip("immutable nginx ingress campaign is opt-in")
	}
	version, err := exec.Command("/usr/sbin/nginx", "-v").CombinedOutput()
	require.NoError(t, err)
	require.Contains(t, string(version), "nginx/1.25.")
	dir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	access := filepath.Join(dir, "access.log")
	config := fmt.Sprintf(`worker_processes 1;
pid %s/nginx.pid;
error_log %s/error.log warn;
events { worker_connections 32; }
http {
 client_body_temp_path %s/client;
 proxy_temp_path %s/proxy;
 fastcgi_temp_path %s/fastcgi;
 uwsgi_temp_path %s/uwsgi;
 scgi_temp_path %s/scgi;
 log_format qualification '$status $request_length';
 access_log %s qualification;
 client_max_body_size %s;
 server {
  listen %s;
  location = /__fixture_ready { access_log off; return 204; }
  location / {
   proxy_connect_timeout 60s;
   proxy_read_timeout 60s;
   proxy_send_timeout 60s;
   proxy_pass %s;
  }
 }
}
`, dir, dir, dir, dir, dir, dir, dir, access, limit, address, backend)
	configPath := filepath.Join(dir, "nginx.conf")
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0o600))
	output := new(bytes.Buffer)
	cmd := exec.Command("/usr/sbin/nginx", "-p", dir+"/", "-c", configPath, "-g", "daemon off;")
	cmd.Stdout, cmd.Stderr = output, output
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		err := cmd.Process.Signal(syscall.SIGQUIT)
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("synthetic nginx quit: %v", err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("synthetic nginx failed: %v: %s", err, output.String())
			}
		case <-time.After(3 * time.Second):
			require.NoError(t, cmd.Process.Kill())
			<-done
			t.Error("synthetic nginx exceeded graceful shutdown budget")
		}
	})
	endpoint := "http://" + address
	client := &http.Client{Timeout: time.Second}
	require.Eventually(t, func() bool {
		response, err := client.Get(endpoint + "/__fixture_ready")
		if err != nil {
			return false
		}
		closeErr := response.Body.Close()
		return closeErr == nil && response.StatusCode == http.StatusNoContent
	}, 3*time.Second, 10*time.Millisecond)
	return endpoint, access
}

// ingressAlert supplies the already-qualified SDK with synthetic existing-shape credentials.
func ingressAlert(t *testing.T, endpoint string) *logSDK.Alert {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	alert, err := logSDK.NewAlert(ctx, endpoint, logSDK.WithAlertType(receiverAlertType), logSDK.WithAlertToken(receiverAlertToken),
		logSDK.WithAlertMaxMessageBytes(1<<20), logSDK.WithAlertPushTimeout(10*time.Second))
	require.NoError(t, err)
	t.Cleanup(func() {
		cancel()
		alert.Close()
		select {
		case <-alert.Done():
		case <-time.After(3 * time.Second):
			t.Error("synthetic ingress SDK sender did not stop")
		}
	})
	return alert
}

// TestAlertIngressEscapedSDKMaximum measures actual wire expansion, not decoded size alone.
func TestAlertIngressEscapedSDKMaximum(t *testing.T) {
	if os.Getenv("GRAPHQL_TEST_ALERT_INGRESS") != "1" {
		t.Skip("immutable nginx ingress campaign is opt-in")
	}
	harness := newReceiverHarness(t, []int{101}, nil, true)
	wireBytes := make(chan int, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		closeErr := r.Body.Close()
		if err != nil || closeErr != nil {
			http.Error(w, "synthetic body read failed", http.StatusBadRequest)
			return
		}
		wireBytes <- len(body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		harness.server.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(upstream.Close)
	endpoint, _ := ingressProxy(t, upstream.URL, "1g")
	alert := ingressAlert(t, endpoint)
	message := strings.Repeat("\x00", 1<<20)
	require.NoError(t, alert.Send(message))
	payload := adoptionDeliveryPayload(t, harness)
	require.Equal(t, receiverAlertType+" >>>>>>>>>>>>>>>>>> \n"+message, payload)
	select {
	case size := <-wireBytes:
		require.Greater(t, size, 6*(1<<20))
		require.Less(t, size, 10*(1<<20), "even the smallest inspected alternate ingress budget must fit this SDK bound")
		t.Logf("decoded SDK bytes=%d; actual GraphQL JSON bytes=%d", len(message), size)
	case <-time.After(time.Second):
		t.Fatal("actual GraphQL wire size was not observed")
	}
}

// TestAlertIngressTooSmallBudgetRejectsBeforeReceiver proves the size control is effective.
// SDK Send acknowledges local admission; remote failure is observed independently.
func TestAlertIngressTooSmallBudgetRejectsBeforeReceiver(t *testing.T) {
	if os.Getenv("GRAPHQL_TEST_ALERT_INGRESS") != "1" {
		t.Skip("immutable nginx ingress campaign is opt-in")
	}
	observedCore, observed := observer.New(zap.DebugLevel)
	logger, err := logSDK.NewWithName("synthetic.ingress", logSDK.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core { return observedCore }))
	require.NoError(t, err)
	previous := logSDK.Shared
	logSDK.Shared = logger
	t.Cleanup(func() { logSDK.Shared = previous })
	harness := newReceiverHarness(t, []int{101}, nil, true)
	var reached atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		harness.server.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(upstream.Close)
	endpoint, access := ingressProxy(t, upstream.URL, "1m")
	alert := ingressAlert(t, endpoint)
	require.NoError(t, alert.Send(strings.Repeat("\x00", 1<<20)))
	require.Eventually(t, func() bool { return observed.FilterMessage("alert delivery failed").Len() == 1 }, 5*time.Second, 10*time.Millisecond)
	alert.Close()
	select {
	case <-alert.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("synthetic rejected sender did not stop")
	}
	require.Zero(t, reached.Load())
	deliveries, failures := harness.bot.snapshot()
	require.Empty(t, deliveries)
	require.Empty(t, failures)
	raw, err := os.ReadFile(access)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	require.Len(t, lines, 1, "no automatic retry may follow the rejection")
	require.True(t, strings.HasPrefix(lines[0], "413 "))
}

// TestAlertIngressActualCrawler crosses actual caller, scoped core, SDK, nginx, GraphQL and Bot HTTP.
func TestAlertIngressActualCrawler(t *testing.T) {
	if os.Getenv("GRAPHQL_TEST_ALERT_INGRESS") != "1" {
		t.Skip("immutable nginx ingress campaign is opt-in")
	}
	harness := newReceiverHarness(t, []int{101}, nil, true)
	endpoint, _ := ingressProxy(t, harness.server.URL, "1g")
	original := strings.Repeat(" \t", 10*1024) + "https://1.1.1.1/Unicode-你好?chosen=original#fragment\n"
	outcome := runAdoptionCaller(t, endpoint, map[string]any{"url": original, "output_markdown": nil}, true, false)
	require.True(t, outcome.result.IsError)
	require.Equal(t, 1, outcome.billings)
	require.Equal(t, 1, outcome.fetches)
	require.Empty(t, outcome.errorOutput)
	fields := adoptionDeliveredFields(t, adoptionDeliveryPayload(t, harness))
	require.Equal(t, original, fields["original_url"])
	require.Equal(t, strings.TrimSpace(original), fields["submitted_url"])
	require.Equal(t, adoptionRequestID, fields["request_id"])
	require.Contains(t, fields, "declared_output_markdown")
	require.Nil(t, fields["declared_output_markdown"])
}
