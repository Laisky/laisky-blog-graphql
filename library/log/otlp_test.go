package log

import (
	"bytes"
	"context"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	collogpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logpb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const syntheticOTLPToken = "synthetic-test-token-only"

// otlpCapture supplies isolated synthetic logging test data.
type otlpCapture struct {
	mu       sync.Mutex
	requests []*collogpb.ExportLogsServiceRequest
	attempts atomic.Int64
	fail     atomic.Bool
	gate     chan struct{}
}

// ServeHTTP supports isolated synthetic logging acceptance.
func (c *otlpCapture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.attempts.Add(1)
	if r.URL.Path != "/v1/logs" || r.Header.Get("Authorization") != "Bearer "+syntheticOTLPToken {
		http.Error(w, "invalid synthetic request", 401)
		return
	}
	if c.gate != nil {
		select {
		case <-c.gate:
		case <-r.Context().Done():
			return
		}
	}
	if c.fail.Load() {
		http.Error(w, "SYNTHETIC_RESPONSE_SECRET", 503)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	request := new(collogpb.ExportLogsServiceRequest)
	if err != nil || proto.Unmarshal(data, request) != nil {
		http.Error(w, "invalid fixture", 400)
		return
	}
	c.mu.Lock()
	c.requests = append(c.requests, request)
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write([]byte{})
}

// records supports isolated synthetic logging acceptance.
func (c *otlpCapture) records() []*logpb.LogRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	var records []*logpb.LogRecord
	for _, request := range c.requests {
		for _, resource := range request.ResourceLogs {
			for _, scope := range resource.ScopeLogs {
				records = append(records, scope.LogRecords...)
			}
		}
	}
	return records
}

// otlpFixture supports isolated synthetic logging acceptance.
func otlpFixture(t *testing.T, capture *otlpCapture) otlpConfig {
	t.Helper()
	server := httptest.NewTLSServer(capture)
	t.Cleanup(server.Close)
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	ca := filepath.Join(dir, "ca.pem")
	require.NoError(t, os.WriteFile(token, []byte(syntheticOTLPToken+"\n"), 0600))
	require.NoError(t, os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600))
	return otlpConfig{endpoint: server.URL + "/v1/logs", tokenFile: token, caFile: ca, host: "test-b1", environment: "test",
		queueSize: 8, batchSize: 2, interval: 10 * time.Millisecond, timeout: 500 * time.Millisecond}
}

// stopOTLP supports isolated synthetic logging acceptance.
func stopOTLP(t *testing.T, runtime *otlpRuntime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, runtime.shutdown(ctx))
}

// TestOTLPInitCapturedChildrenAndConfigureIdempotence verifies the isolated logging lifecycle contract.
func TestOTLPInitCapturedChildrenAndConfigureIdempotence(t *testing.T) {
	configureOTLPMu.Lock()
	old := configuredOTLP
	configuredOTLP = nil
	configureOTLPMu.Unlock()
	oldActive := activeOTLP.Swap(nil)
	t.Cleanup(func() {
		configureOTLPMu.Lock()
		configuredOTLP = old
		configureOTLPMu.Unlock()
		activeOTLP.Store(oldActive)
	})
	capture := new(otlpCapture)
	config := otlpFixture(t, capture)
	for k, v := range map[string]string{
		"APP_LOG_OTLP_ENDPOINT": config.endpoint, "APP_LOG_OTLP_TOKEN_FILE": config.tokenFile, "APP_LOG_OTLP_CA_FILE": config.caFile,
		"APP_LOG_OTLP_HOST": config.host, "APP_LOG_OTLP_ENV": config.environment,
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT": "http://127.0.0.1:1/forbidden",
		"OTEL_EXPORTER_OTLP_HEADERS":       "Authorization=SYNTHETIC_ENV_SECRET",
		"OTEL_EXPORTER_OTLP_LOGS_HEADERS":  "X-Private=SYNTHETIC_ENV_SECRET",
		"OTEL_EXPORTER_OTLP_CERTIFICATE":   "/not-read/synthetic-env-ca",
		"OTEL_RESOURCE_ATTRIBUTES":         "service.name=SYNTHETIC_RESOURCE_SECRET,host.name=SYNTHETIC_HOST_SPOOF",
		"OTEL_SERVICE_NAME":                "SYNTHETIC_SERVICE_SPOOF",
	} {
		t.Setenv(k, v)
	}
	var stdout bytes.Buffer
	base, err := logSDK.NewWithName("graphql", logSDK.LevelInfo, zap.WrapCore(func(zapcore.Core) zapcore.Core {
		return zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&stdout), zapcore.InfoLevel)
	}), privateOTLPOption())
	require.NoError(t, err)
	captured := base.Named("captured-before-provider").With(zap.Int("attempts", 1), zap.String("request_body", "SYNTHETIC_BOUND_SECRET"))
	captured.Info("before-provider")
	shutdown, err := ConfigureOTLP(context.Background())
	require.NoError(t, err)
	second, err := ConfigureOTLP(context.Background())
	require.NoError(t, err)
	captured.Info("operation completed password=SYNTHETIC_STDOUT_SECRET", zap.Int("status", 200))
	captured.Debug("filtered")
	require.NoError(t, shutdown(context.Background()))
	require.NoError(t, second(context.Background()))
	captured.Info("after-shutdown")
	records := capture.records()
	require.Len(t, records, 1)
	require.Contains(t, stdout.String(), "SYNTHETIC_STDOUT_SECRET")
	require.Contains(t, stdout.String(), "SYNTHETIC_BOUND_SECRET")
	require.Len(t, strings.Split(strings.TrimSpace(stdout.String()), "\n"), 3)
	capture.mu.Lock()
	resource := capture.requests[0].ResourceLogs[0]
	capture.mu.Unlock()
	attrs := map[string]string{}
	for _, a := range resource.Resource.Attributes {
		attrs[a.Key] = a.Value.GetStringValue()
	}
	require.Equal(t, map[string]string{"service.name": "graphql", "service.namespace": "laisky", "host.name": "test-b1", "deployment.environment.name": "test"}, attrs)
	require.Equal(t, OTLPStats{Emitted: 1, ExportedRecords: 1}, SnapshotOTLP())
}

// TestOTLPRetryAndProcessRestartBoundary verifies the isolated logging lifecycle contract.
func TestOTLPRetryAndProcessRestartBoundary(t *testing.T) {
	capture := new(otlpCapture)
	capture.fail.Store(true)
	config := otlpFixture(t, capture)
	runtime, err := newOTLPRuntime(context.Background(), config)
	require.NoError(t, err)
	require.NoError(t, runtime.emit(zapcore.Entry{Level: zapcore.InfoLevel, Time: time.Now()}, []zapcore.Field{zap.Int("attempt", 1)}))
	require.Eventually(t, func() bool { return capture.attempts.Load() >= 1 }, time.Second, 5*time.Millisecond)
	capture.fail.Store(false)
	require.Eventually(t, func() bool { return len(capture.records()) == 1 }, time.Second, 5*time.Millisecond)
	stopOTLP(t, runtime)
	// A fresh SDK represents process restart. Accepted local SDK records are volatile;
	// only collector-acknowledged records have crossed the durable boundary.
	runtime, err = newOTLPRuntime(context.Background(), config)
	require.NoError(t, err)
	require.NoError(t, runtime.emit(zapcore.Entry{Level: zapcore.InfoLevel, Time: time.Now()}, []zapcore.Field{zap.Int("attempt", 2)}))
	stopOTLP(t, runtime)
	require.Len(t, capture.records(), 2)
}

// TestOTLPBoundedQueueAndShutdown verifies the isolated logging lifecycle contract.
func TestOTLPBoundedQueueAndShutdown(t *testing.T) {
	capture := &otlpCapture{gate: make(chan struct{})}
	config := otlpFixture(t, capture)
	config.queueSize = 4
	config.batchSize = 2
	config.timeout = 100 * time.Millisecond
	runtime, err := newOTLPRuntime(context.Background(), config)
	require.NoError(t, err)
	started := time.Now()
	for i := range 1000 {
		require.NoError(t, runtime.emit(zapcore.Entry{Level: zapcore.InfoLevel, Time: time.Now()}, []zapcore.Field{zap.Int("attempt", i)}))
	}
	require.Less(t, time.Since(started), 500*time.Millisecond, "logger must not wait for exporter I/O")
	require.LessOrEqual(t, capture.attempts.Load(), int64(2), "one bounded SDK worker owns export")
	close(capture.gate)
	stopOTLP(t, runtime)
	stats := runtime.stats()
	require.Equal(t, uint64(1000), stats.Emitted)
	require.Greater(t, stats.Unresolved, uint64(0), "clean drain reveals overflow losses")
	require.LessOrEqual(t, stats.ExportedRecords+stats.FailedExportRecords, uint64(config.queueSize+config.batchSize*2))
}

// TestOTLPShutdownDeadlineAndSanitizedFailure verifies the isolated logging lifecycle contract.
func TestOTLPShutdownDeadlineAndSanitizedFailure(t *testing.T) {
	capture := &otlpCapture{gate: make(chan struct{})}
	config := otlpFixture(t, capture)
	config.timeout = 200 * time.Millisecond
	runtime, err := newOTLPRuntime(context.Background(), config)
	require.NoError(t, err)
	require.NoError(t, runtime.emit(zapcore.Entry{Level: zapcore.InfoLevel, Time: time.Now()}, nil))
	require.Eventually(t, func() bool { return capture.attempts.Load() > 0 }, time.Second, 5*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = runtime.shutdown(ctx)
	require.Error(t, err)
	require.NotContains(t, err.Error(), syntheticOTLPToken)
	require.Less(t, time.Since(started), 150*time.Millisecond)
	close(capture.gate)
	require.Eventually(t, func() bool { return runtime.stats().ExportedRecords+runtime.stats().FailedExportRecords > 0 }, time.Second, 5*time.Millisecond)
	capture.fail.Store(true)
	failureRuntime := &otlpRuntime{}
	exporter := &privateExporter{delegate: failPrivateExporter{}, runtime: failureRuntime}
	err = exporter.Export(context.Background(), []sdklog.Record{{}})
	require.EqualError(t, err, "application OTLP export failed")
	require.NotContains(t, err.Error(), "SYNTHETIC_RESPONSE_SECRET")
	require.Equal(t, uint64(1), failureRuntime.stats().FailedExportRecords)
}

// failPrivateExporter supplies isolated synthetic logging test data.
type failPrivateExporter struct{}

// Export supports isolated synthetic logging acceptance.
func (failPrivateExporter) Export(context.Context, []sdklog.Record) error { return io.ErrUnexpectedEOF }

// ForceFlush supports isolated synthetic logging acceptance.
func (failPrivateExporter) ForceFlush(context.Context) error { return nil }

// Shutdown supports isolated synthetic logging acceptance.
func (failPrivateExporter) Shutdown(context.Context) error { return nil }

// TestOTLPConfigRejectsUnsafeInputs verifies the isolated logging lifecycle contract.
func TestOTLPConfigRejectsUnsafeInputs(t *testing.T) {
	capture := new(otlpCapture)
	valid := otlpFixture(t, capture)
	for _, tc := range []struct {
		name   string
		mutate func(*otlpConfig)
	}{
		{"plain HTTP", func(c *otlpConfig) { c.endpoint = "http://127.0.0.1:4318/v1/logs" }},
		{"credentials in URL", func(c *otlpConfig) { c.endpoint = "https://user:secret@example.invalid/v1/logs" }},
		{"query", func(c *otlpConfig) { c.endpoint += "?token=secret" }},
		{"wrong path", func(c *otlpConfig) { c.endpoint += "/other" }},
		{"empty host label", func(c *otlpConfig) { c.host = "" }},
		{"missing token", func(c *otlpConfig) { c.tokenFile = "/missing/synthetic" }},
		{"invalid CA", func(c *otlpConfig) { c.caFile = c.tokenFile }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := valid
			tc.mutate(&config)
			runtime, err := newOTLPRuntime(context.Background(), config)
			require.Error(t, err)
			require.Nil(t, runtime)
			require.NotContains(t, err.Error(), "user:secret")
		})
	}
}

// TestOTLPConcurrentEmitAndShutdown verifies the isolated logging lifecycle contract.
func TestOTLPConcurrentEmitAndShutdown(t *testing.T) {
	capture := new(otlpCapture)
	runtime, err := newOTLPRuntime(context.Background(), otlpFixture(t, capture))
	require.NoError(t, err)
	var workers sync.WaitGroup
	outcomes := make(chan error, 400)
	for range 4 {
		workers.Go(func() {
			for range 100 {
				outcomes <- runtime.emit(zapcore.Entry{Level: zapcore.InfoLevel, Time: time.Now()}, nil)
			}
		})
	}
	var shutdownErr error
	workers.Go(func() { shutdownErr = runtime.shutdown(context.Background()) })
	workers.Wait()
	close(outcomes)
	require.NoError(t, shutdownErr)
	var rejected uint64
	for err := range outcomes {
		if err != nil {
			rejected++
			require.Equal(t, "application OTLP pipeline stopped before record admission", err.Error())
		}
	}
	require.Equal(t, rejected, runtime.stats().Rejected)
	require.NoError(t, runtime.shutdown(context.Background()))
}

// TestOTLPRejectsCallbacksAfterShutdown verifies stale hook references cannot emit after drain.
func TestOTLPRejectsCallbacksAfterShutdown(t *testing.T) {
	capture := new(otlpCapture)
	runtime, err := newOTLPRuntime(context.Background(), otlpFixture(t, capture))
	require.NoError(t, err)
	require.NoError(t, runtime.emit(zapcore.Entry{Level: zapcore.InfoLevel, Time: time.Now()}, nil))
	stopOTLP(t, runtime)
	before := runtime.stats()
	require.NoError(t, runtime.emit(zapcore.Entry{Level: zapcore.ErrorLevel, Time: time.Now()}, nil))
	require.Equal(t, before, runtime.stats())
	require.Len(t, capture.records(), 1)
}

// TestOTLPRedirectCannotForwardBearer verifies redirection never forwards the private credential.
func TestOTLPRedirectCannotForwardBearer(t *testing.T) {
	var redirected atomic.Int64
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); w.WriteHeader(200) }))
	defer other.Close()
	capture := new(otlpCapture)
	config := otlpFixture(t, capture)
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/v1/logs", http.StatusTemporaryRedirect)
	}))
	defer receiver.Close()
	config.endpoint = receiver.URL + "/v1/logs"
	require.NoError(t, os.WriteFile(config.caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: receiver.Certificate().Raw}), 0600))
	runtime, err := newOTLPRuntime(context.Background(), config)
	require.NoError(t, err)
	require.NoError(t, runtime.emit(zapcore.Entry{Level: zapcore.InfoLevel, Time: time.Now()}, nil))
	require.Error(t, runtime.shutdown(context.Background()))
	require.Zero(t, redirected.Load())
	require.Equal(t, uint64(1), runtime.stats().FailedExportRecords)
}

// TestOTLPPartialResponseIsNotDurableAcceptance documents SDK success versus receiver acceptance.
func TestOTLPPartialResponseIsNotDurableAcceptance(t *testing.T) {
	capture := new(otlpCapture)
	config := otlpFixture(t, capture)
	response, err := proto.Marshal(&collogpb.ExportLogsServiceResponse{PartialSuccess: &collogpb.ExportLogsPartialSuccess{
		RejectedLogRecords: 1, ErrorMessage: "SYNTHETIC_PARTIAL_RESPONSE_SECRET",
	}})
	require.NoError(t, err)
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(response)
	}))
	defer receiver.Close()
	config.endpoint = receiver.URL + "/v1/logs"
	require.NoError(t, os.WriteFile(config.caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: receiver.Certificate().Raw}), 0600))
	runtime, err := newOTLPRuntime(context.Background(), config)
	require.NoError(t, err)
	require.NoError(t, runtime.emit(zapcore.Entry{Level: zapcore.InfoLevel, Time: time.Now()}, nil))
	require.Error(t, runtime.shutdown(context.Background()))
	stats := runtime.stats()
	require.Zero(t, stats.ExportedRecords)
	require.Equal(t, uint64(1), stats.FailedExportRecords)
	require.Zero(t, stats.Unresolved)
	// The SDK handles partial responses; local counters alone cannot prove searchable delivery.
}

// TestOTLPDisabledIgnoresUnusedCredentialFiles verifies opt-in behavior without secret reads.
func TestOTLPDisabledIgnoresUnusedCredentialFiles(t *testing.T) {
	t.Setenv("APP_LOG_OTLP_ENDPOINT", "")
	t.Setenv("APP_LOG_OTLP_TOKEN_FILE", "/must-not-read/synthetic")
	t.Setenv("APP_LOG_OTLP_CA_FILE", "/must-not-read/synthetic")
	shutdown, err := ConfigureOTLP(context.Background())
	require.NoError(t, err)
	require.NoError(t, shutdown(context.Background()))
}

// TestOTLPContentFidelity retains authored operational content rather than a generic replacement.
func TestOTLPContentFidelity(t *testing.T) {
	capture := new(otlpCapture)
	runtime, err := newOTLPRuntime(context.Background(), otlpFixture(t, capture))
	require.NoError(t, err)
	previous := activeOTLP.Swap(runtime)
	defer activeOTLP.Store(previous)
	logger, err := logSDK.New(logSDK.WithName("graphql"), logSDK.WithOutputPaths([]string{}), logSDK.WithZapOptions(privateOTLPOption()))
	require.NoError(t, err)
	logger.With(zap.String("stage", "cache")).Info("synthetic operational refresh completed",
		zap.String("future_revision", "parser-v2"), zap.Int("channel_count", 17),
		zap.Float64("duration_ms", 12.5), zap.Bool("cache_ready", true))
	stopOTLP(t, runtime)
	records := capture.records()
	require.Len(t, records, 1)
	require.Equal(t, "synthetic operational refresh completed", records[0].Body.GetStringValue())
	attrs := map[string]*commonpb.AnyValue{}
	for _, a := range records[0].Attributes {
		attrs[a.Key] = a.Value
	}
	require.Equal(t, "cache", attrs["stage"].GetStringValue())
	require.Equal(t, "parser-v2", attrs["future_revision"].GetStringValue())
	require.Equal(t, int64(17), attrs["channel_count"].GetIntValue())
	require.Equal(t, 12.5, attrs["duration_ms"].GetDoubleValue())
	require.True(t, attrs["cache_ready"].GetBoolValue())
}

// TestOTLPExistingStartupMessageAndFutureFields preserves real startup diagnostics and benign names.
func TestOTLPExistingStartupMessageAndFutureFields(t *testing.T) {
	capture := new(otlpCapture)
	runtime, err := newOTLPRuntime(context.Background(), otlpFixture(t, capture))
	require.NoError(t, err)
	require.NoError(t, runtime.emit(zapcore.Entry{Level: zapcore.InfoLevel, Time: time.Now(), Message: "parallel database initialization completed"},
		[]zapcore.Field{
			zap.Duration("duration", 15*time.Millisecond),
			zap.Time("completed_at", time.Date(2026, 10, 18, 12, 0, 0, 123, time.FixedZone("private-label", 3600))), zap.String("future_revision", "parser-v2"),
			zap.Int("token_count", 42), zap.Bool("password_reset_required", true), zap.String("query_latency", "25ms"),
			zap.Int("auth_failure_count_v2", 2), zap.String("stage", "future-stage-v8"),
			zap.String("service.name", "SYNTHETIC_SPOOF_SERVICE"), zap.String("host.name", "SYNTHETIC_SPOOF_HOST"),
			zap.String("deployment.environment.name", "SYNTHETIC_SPOOF_ENV"),
			zap.String("current_input", "SYNTHETIC_INPUT_SECRET"), zap.String("conversation_items", "SYNTHETIC_CONVERSATION_SECRET"),
			zap.String("input_items", "SYNTHETIC_INPUT_ITEMS_SECRET"), zap.String("output_items", "SYNTHETIC_OUTPUT_ITEMS_SECRET"),
		}))
	stopOTLP(t, runtime)
	records := capture.records()
	require.Len(t, records, 1)
	require.Equal(t, "parallel database initialization completed", records[0].Body.GetStringValue())
	attrs := map[string]*commonpb.AnyValue{}
	for _, a := range records[0].Attributes {
		attrs[a.Key] = a.Value
	}
	require.Equal(t, int64(15*time.Millisecond), attrs["duration"].GetIntValue())
	require.Equal(t, "2026-10-18T12:00:00.000000123+01:00", attrs["completed_at"].GetStringValue())
	require.Equal(t, "parser-v2", attrs["future_revision"].GetStringValue())
	require.Equal(t, int64(42), attrs["token_count"].GetIntValue())
	require.True(t, attrs["password_reset_required"].GetBoolValue())
	require.Equal(t, "25ms", attrs["query_latency"].GetStringValue())
	require.Equal(t, int64(2), attrs["auth_failure_count_v2"].GetIntValue())
	require.Equal(t, "future-stage-v8", attrs["stage"].GetStringValue())
	encoded, err := protojson.Marshal(records[0])
	require.NoError(t, err)
	require.Contains(t, string(encoded), "SYNTHETIC_INPUT_SECRET")
	require.Contains(t, string(encoded), "SYNTHETIC_CONVERSATION_SECRET")
	require.Contains(t, string(encoded), "SYNTHETIC_SPOOF_SERVICE")
}

// TestOTLPResourceEnvironmentFailsClosed rejects private and malformed metadata before SDK construction.
func TestOTLPResourceEnvironmentFailsClosed(t *testing.T) {
	for _, raw := range []string{
		"SYNTHETIC_MALFORMED_RESOURCE_SECRET",
		"service.name=%SYNTHETIC_BAD_ESCAPE_SECRET",
		strings.Repeat("SYNTHETIC_OVERSIZED_SECRET", 1000),
	} {
		t.Run(fmt.Sprint(len(raw)), func(t *testing.T) {
			t.Setenv("OTEL_RESOURCE_ATTRIBUTES", raw)
			capture := new(otlpCapture)
			config := otlpFixture(t, capture)
			runtime, err := newOTLPRuntime(context.Background(), config)
			require.Error(t, err)
			require.Nil(t, runtime)
			require.NotContains(t, err.Error(), "SYNTHETIC_")
			require.Zero(t, capture.attempts.Load())
		})
	}
}

// TestOTLPDebugChildFollowsSourceLevel preserves intentionally enabled diagnostics without enabling filtered logs.
func TestOTLPDebugChildFollowsSourceLevel(t *testing.T) {
	capture := new(otlpCapture)
	runtime, err := newOTLPRuntime(context.Background(), otlpFixture(t, capture))
	require.NoError(t, err)
	previous := activeOTLP.Swap(runtime)
	defer activeOTLP.Store(previous)
	logger, err := logSDK.New(logSDK.WithName("graphql"), logSDK.WithLevel(logSDK.LevelInfo),
		logSDK.WithOutputPaths([]string{}), logSDK.WithZapOptions(privateOTLPOption()))
	require.NoError(t, err)
	child := logger.Named("request").With(zap.String("stage", "inspection"))
	child.Debug("must remain filtered")
	require.NoError(t, logger.ChangeLevel(logSDK.LevelDebug))
	child.Debug("debug inspection completed", zap.String("future_revision", "parser-v2"))
	stopOTLP(t, runtime)
	records := capture.records()
	require.Len(t, records, 2)
	bodies := map[string]int{}
	for _, record := range records {
		bodies[record.Body.GetStringValue()]++
		require.Equal(t, logpb.SeverityNumber_SEVERITY_NUMBER_DEBUG, record.SeverityNumber)
	}
	require.Equal(t, map[string]int{"set logger level": 1, "debug inspection completed": 1}, bodies)
	require.NotContains(t, bodies, "must remain filtered")
}
