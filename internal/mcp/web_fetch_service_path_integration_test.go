package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/calllog"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	appLog "github.com/Laisky/laisky-blog-graphql/library/log"
)

// serviceBillingRequest records synthetic quota mutations at the owned HTTP sink.
type serviceBillingRequest struct {
	Phase  string
	Cost   int
	Reason string
}

// TestMCPWebFetchServicePath joins actual HTTP MCP and billing sockets, a real
// disposable redis-server, and a separate Ramjet test-binary worker. The worker
// replaces Chrome with the existing local egress-fetch fixture and synthetic
// RemoteAddr; MCP call-log persistence uses a recorder. This does not attest an
// unmodified production image, browser execution, real public peers, or SQL.
//
// Compile the sibling worker under the validation lease, then run:
// CRAWLER_SERVICE_TEST_WORKER_BINARY=/tmp/ramjet-service.test go test -count=1
// -timeout 3m ./internal/mcp -run '^TestMCPWebFetchServicePath$' -v
func TestMCPWebFetchServicePath(t *testing.T) {
	binary := os.Getenv("CRAWLER_SERVICE_TEST_WORKER_BINARY")
	if binary == "" {
		t.Skip("CRAWLER_SERVICE_TEST_WORKER_BINARY must name the compiled sibling Ramjet tasks test binary")
	}
	require.True(t, filepath.IsAbs(binary))
	info, err := os.Stat(binary)
	require.NoError(t, err)
	require.False(t, info.IsDir())
	redisExecutable, err := exec.LookPath("redis-server")
	require.NoError(t, err, "real redis-server is required; no fake-server fallback")
	dir := t.TempDir()
	socket := filepath.Join(dir, "redis.sock")
	redisLog := filepath.Join(dir, "redis.log")
	redisProcess := exec.Command(redisExecutable, "--port", "0", "--bind", "127.0.0.1",
		"--unixsocket", socket, "--unixsocketperm", "700", "--save", "", "--appendonly", "no",
		"--dir", dir, "--logfile", redisLog)
	startServiceChild(t, redisProcess)
	store := rlibs.NewDB(&redis.Options{Network: "unix", Addr: socket, Protocol: 2, DisableIdentity: true, MaxRetries: -1})
	client := store.GetDB().Client
	t.Cleanup(func() { require.NoError(t, store.GetDB().Close()) })
	require.Eventually(t, func() bool {
		pingCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		return client.Ping(pingCtx).Err() == nil
	}, 5*time.Second, 25*time.Millisecond, "owned redis-server did not start")
	ctx := t.Context()
	require.NoError(t, client.RPush(ctx, rlibs.KeyTaskHTMLCrawlerLegacyPending, "owned-legacy-sentinel").Err())

	alertMessages := make(chan string, 32)
	alertSink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Variables struct {
				Message string `json:"msg"`
			} `json:"variables"`
		}
		decodeErr := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&payload)
		require.NoError(t, decodeErr)
		alertMessages <- payload.Variables.Message
		w.Header().Set("Content-Type", "application/json")
		_, writeErr := io.WriteString(w, "{\"data\":{\"TelegramMonitorAlert\":{\"name\":\"accepted\"}}}")
		require.NoError(t, writeErr)
	}))
	t.Cleanup(alertSink.Close)

	workerLogPath := filepath.Join(dir, "worker.log")
	workerLog, err := os.Create(workerLogPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, workerLog.Close()) })
	worker := exec.Command(binary, "-test.run=^TestCrawlerServicePathWorker$", "-test.timeout=2m", "-test.v")
	worker.Env = append(os.Environ(), "CRAWLER_SERVICE_TEST_REDIS_SOCKET="+socket,
		"CRAWLER_SERVICE_TEST_ALERT_URL="+alertSink.URL)
	worker.Stdout, worker.Stderr = workerLog, workerLog
	workerDone := startServiceChild(t, worker)
	require.Eventually(t, func() bool {
		return client.Get(ctx, "service-path/worker-ready").Val() == "ready"
	}, 10*time.Second, 25*time.Millisecond, "worker did not initialize; inspect %s", workerLogPath)

	bills := make(chan serviceBillingRequest, 16)
	var billingStatus atomic.Int32
	billingStatus.Store(http.StatusOK)
	billingSink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/token/consume", r.URL.Path)
		require.Equal(t, "Bearer sk-service-secret-canary", r.Header.Get("Authorization"))
		var raw struct {
			Phase  string `json:"phase"`
			Cost   int    `json:"add_used_quota"`
			Reason string `json:"add_reason"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&raw))
		bills <- serviceBillingRequest{Phase: raw.Phase, Cost: raw.Cost, Reason: raw.Reason}
		w.WriteHeader(int(billingStatus.Load()))
		_, writeErr := io.WriteString(w, "synthetic billing response service-billing-body-canary")
		require.NoError(t, writeErr)
	}))
	t.Cleanup(billingSink.Close)
	originalBillingAPI := oneapi.BillingAPI
	oneapi.BillingAPI = billingSink.URL
	t.Cleanup(func() { oneapi.BillingAPI = originalBillingAPI })

	operationLogPath := filepath.Join(dir, "operation.log")
	operationLog, err := os.Create(operationLogPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, operationLog.Close()) })
	alert, err := glog.NewAlert(ctx, alertSink.URL, glog.WithAlertType("synthetic-service-path"),
		glog.WithAlertToken("synthetic-alert-only"))
	require.NoError(t, err)
	t.Cleanup(func() { alert.Close(); <-alert.Done() })
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(operationLog), zap.DebugLevel)
	logger, err := glog.NewWithName("graphql.laisky-graphql", glog.LevelDebug,
		zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }),
		zap.HooksWithFields(appLog.WebDiagnosticAlertHook(alert.GetZapHook())))
	require.NoError(t, err)
	recorder := &behaviorRecorder{}
	server, err := NewServer(nil, nil, nil, nil, rag.Settings{}, nil, nil, store, recorder,
		ToolsSettings{WebFetchEnabled: true}, logger)
	require.NoError(t, err)
	endpoint := httptest.NewServer(server.Handler())
	t.Cleanup(endpoint.Close)

	cases := []struct {
		path                         string
		markdown                     bool
		code, workerCategory, status string
	}{
		{path: "/html", status: rlibs.TaskStatusSuccess},
		{path: "/markdown", markdown: true, status: rlibs.TaskStatusSuccess},
		{path: "/quoted-challenge", status: rlibs.TaskStatusSuccess},
		{path: "/challenge", code: "crawler_failed", workerCategory: "challenge", status: rlibs.TaskStatusFailed},
		{path: "/render-failure", code: "crawler_failed", workerCategory: "render_failed", status: rlibs.TaskStatusFailed},
		{path: "/missing-worker-receipt", code: "crawler_failed", workerCategory: "evidence_rejected", status: rlibs.TaskStatusFailed},
		{path: "/missing-result-receipt", code: "crawler_egress_unverified", status: rlibs.TaskStatusSuccess},
		{path: "/forged-result-peer", code: "crawler_egress_rejected", status: rlibs.TaskStatusSuccess},
	}
	for _, tc := range cases {
		t.Run(strings.TrimPrefix(tc.path, "/"), func(t *testing.T) {
			target := "http://93.184.216.34" + tc.path + "?private=service-query-canary"
			result := serviceMCPCall(t, endpoint.URL, target, tc.markdown, true)
			require.Equal(t, tc.code != "", result.IsError)
			text := getText(t, result)
			if tc.code == "" {
				require.Contains(t, text, "service-body-canary")
			} else {
				require.NotContains(t, text, "service-body-canary")
				require.NotContains(t, text, "service-secret-canary")
			}
			requireServiceBill(t, bills, oneapi.PriceWebFetch.Int())
			audit := recorder.last()
			metadata := billingMetadata(t, audit)
			require.Equal(t, string(oneapi.BillingAccepted), metadata.Outcome)
			require.True(t, metadata.Charged)
			require.False(t, metadata.Indeterminate)
			require.Equal(t, oneapi.PriceWebFetch.Int(), audit.Cost)
			expectedAuditStatus := calllog.StatusSuccess
			if tc.code != "" {
				expectedAuditStatus = calllog.StatusError
			}
			require.Equal(t, expectedAuditStatus, audit.Status)

			id := client.Get(ctx, "service-path/case"+tc.path).Val()
			require.NotEmpty(t, id)
			raw, resultErr := client.Get(ctx, rlibs.KeyPrefixTaskHTMLCrawlerResult+id).Result()
			require.NoError(t, resultErr)
			decoded, decodeErr := rlibs.NewHTMLCrawlerTaskFromString(raw)
			require.NoError(t, decodeErr)
			require.Equal(t, id, decoded.TaskID)
			require.Equal(t, target, decoded.Url)
			require.Equal(t, tc.markdown, decoded.OutputMarkdown)
			require.Equal(t, tc.status, decoded.Status)
			require.Empty(t, decoded.APIKey)
			require.Equal(t, []string{"93.184.216.34"}, decoded.Egress.Addresses)
			require.Greater(t, client.TTL(ctx, rlibs.KeyPrefixTaskHTMLCrawlerResult+id).Val(), 2*time.Hour)
			if tc.workerCategory != "" {
				var wire map[string]any
				require.NoError(t, json.Unmarshal([]byte(raw), &wire))
				require.Equal(t, tc.workerCategory, wire["failure_category"])
			}
			if tc.code == "" {
				require.NotNil(t, decoded.EgressReceipt)
				require.Equal(t, "93.184.216.34", decoded.EgressReceipt.Origins[0].PeerAddress)
			}
			if tc.code != "" {
				logData, readErr := os.ReadFile(operationLogPath)
				require.NoError(t, readErr)
				require.Contains(t, string(logData), "\"error_code\":\""+tc.code+"\"")
				if tc.workerCategory != "" {
					require.Contains(t, string(logData), "\"worker_category\":\""+tc.workerCategory+"\"")
				}
			}
		})
	}

	t.Run("billing-denial", func(t *testing.T) {
		billingStatus.Store(http.StatusForbidden)
		result := serviceMCPCall(t, endpoint.URL, "http://93.184.216.34/denied", true, true)
		require.True(t, result.IsError)
		requireServiceBill(t, bills, oneapi.PriceWebFetch.Int())
		audit := recorder.last()
		require.Equal(t, string(oneapi.BillingDenied), billingMetadata(t, audit).Outcome)
		require.False(t, audit.Billing.Charged)
		require.Zero(t, audit.Cost)
		require.Zero(t, client.LLen(ctx, rlibs.KeyTaskHTMLCrawlerPending).Val())
		require.Zero(t, client.Exists(ctx, "service-path/case/denied").Val())
		billingStatus.Store(http.StatusOK)
	})
	t.Run("private-admission", func(t *testing.T) {
		result := serviceMCPCall(t, endpoint.URL, "http://127.0.0.1/private", true, true)
		require.True(t, result.IsError)
		// Validation keeps the established centralized zero-cost audit, while no
		// paid admission or worker task occurs.
		requireServiceBill(t, bills, 0)
		require.Equal(t, string(oneapi.BillingNotAttempted), recorder.last().Billing.Outcome)
		require.Zero(t, recorder.last().Cost)
		require.Zero(t, client.LLen(ctx, rlibs.KeyTaskHTMLCrawlerPending).Val())
	})
	t.Run("missing-bearer", func(t *testing.T) {
		result := serviceMCPCall(t, endpoint.URL, "http://93.184.216.34/no-auth", true, false)
		require.True(t, result.IsError)
		require.Equal(t, string(oneapi.BillingNotAttempted), recorder.last().Billing.Outcome)
		require.Zero(t, recorder.last().Cost)
		pending, queueErr := client.LLen(ctx, rlibs.KeyTaskHTMLCrawlerPending).Result()
		require.NoError(t, queueErr)
		require.Zero(t, pending)
	})
	resultKeys, keysErr := client.Keys(ctx, rlibs.KeyPrefixTaskHTMLCrawlerResult+"*").Result()
	require.NoError(t, keysErr)
	require.Len(t, resultKeys, 8)
	select {
	case workerErr := <-workerDone:
		workerData, readErr := os.ReadFile(workerLogPath)
		require.NoError(t, readErr)
		require.NoError(t, workerErr, "worker output: %s", workerData)
	case <-time.After(10 * time.Second):
		t.Fatal("worker failed to complete owned tasks")
	}
	require.Equal(t, []string{"owned-legacy-sentinel"}, client.LRange(ctx, rlibs.KeyTaskHTMLCrawlerLegacyPending, 0, -1).Val())
	require.Zero(t, client.LLen(ctx, rlibs.KeyTaskHTMLCrawlerPending).Val())
	require.Equal(t, "done", client.Get(ctx, "service-path/worker-done").Val())
	for _, path := range []string{operationLogPath, workerLogPath} {
		data, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		for _, canary := range []string{"service-secret-canary", "service-query-canary", "service-body-canary", "service-billing-body-canary"} {
			require.NotContains(t, string(data), canary, "private fixture content escaped to logs")
		}
	}
	var producerAlert, workerAlert bool
	alertDeadline := time.NewTimer(5 * time.Second)
	defer alertDeadline.Stop()
	for !producerAlert || !workerAlert || len(alertMessages) > 0 {
		select {
		case message := <-alertMessages:
			for _, canary := range []string{"service-secret-canary", "service-query-canary", "service-body-canary", "service-billing-body-canary"} {
				require.NotContains(t, message, canary)
			}
			isProducer := strings.Contains(message, "graphql.laisky-graphql.mcp.web_fetch")
			isWorker := strings.Contains(message, "go-ramjet.run_dynamic_web_crawler.verified_crawler_task")
			if !isProducer && !isWorker {
				continue // Other application scopes intentionally remain metadata-only.
			}
			var fields map[string]any
			require.NoError(t, json.Unmarshal([]byte(message[strings.LastIndex(message, "\n")+1:]), &fields))
			require.NotEmpty(t, fields, "reviewed operation fields must survive the actual SDK HTTP request")
			require.NotEmpty(t, fields["stage"])
			require.NotEmpty(t, fields["error_code"])
			if isWorker {
				require.NotEmpty(t, fields["category"])
				require.NotEmpty(t, fields["task_id"], "worker alerts need the actual queued correlation ID")
			}
			producerAlert = producerAlert || isProducer
			workerAlert = workerAlert || isWorker
		case <-alertDeadline.C:
			t.Fatal("actual producer and worker alerts must both retain useful structured fields")
		}
	}
	select {
	case unexpected := <-bills:
		t.Fatalf("duplicate billing event: %+v", unexpected)
	default:
	}
}

// startServiceChild starts an owned process and always reaps it on test cleanup.
func startServiceChild(t *testing.T, cmd *exec.Cmd) <-chan error {
	t.Helper()
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		select {
		case <-done:
			return
		default:
		}
		if err := cmd.Process.Kill(); err != nil {
			t.Logf("owned process already stopped: %v", err)
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("owned subprocess failed to stop")
		}
	})
	return done
}

// requireServiceBill checks the next actual HTTP billing request.
func requireServiceBill(t *testing.T, bills <-chan serviceBillingRequest, cost int) {
	t.Helper()
	select {
	case request := <-bills:
		require.Equal(t, "single", request.Phase)
		require.Equal(t, "web_fetch", request.Reason)
		require.Equal(t, cost, request.Cost)
	case <-time.After(5 * time.Second):
		t.Fatal("billing HTTP receiver saw no request")
	}
}

// serviceMCPCall sends a stateless protocol request over an actual HTTP socket.
func serviceMCPCall(t *testing.T, endpoint, target string, markdown, authenticated bool) *mcpgo.CallToolResult {
	t.Helper()
	params := map[string]any{"name": "web_fetch", "arguments": map[string]any{"url": target, "output_markdown": markdown},
		"_meta": map[string]any{mcpgo.MetaKeyProtocolVersion: mcpgo.ProtocolVersion20260728,
			mcpgo.MetaKeyClientInfo:         map[string]any{"name": "service-path-fixture", "version": "1"},
			mcpgo.MetaKeyClientCapabilities: map[string]any{}}}
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": mcpgo.MethodToolsCall, "params": params})
	require.NoError(t, err)
	requestCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint+"/mcp/", bytes.NewReader(raw))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set(mcpgo.HeaderProtocolVersion, mcpgo.ProtocolVersion20260728)
	request.Header.Set(mcpgo.HeaderMethod, string(mcpgo.MethodToolsCall))
	request.Header.Set(mcpgo.HeaderName, "web_fetch")
	if authenticated {
		request.Header.Set("Authorization", "Bearer sk-service-secret-canary")
	}
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	require.NoError(t, err)
	if strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		scanner := bufio.NewScanner(bytes.NewReader(body))
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				body = []byte(strings.TrimPrefix(scanner.Text(), "data: "))
				break
			}
		}
		require.NoError(t, scanner.Err())
	}
	var envelope struct {
		Result json.RawMessage
		Error  json.RawMessage
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	require.Empty(t, envelope.Error, "protocol rejected owned request: %s", body)
	var wire struct {
		IsError bool `json:"isError"`
		Content []struct{ Type, Text string }
	}
	require.NoError(t, json.Unmarshal(envelope.Result, &wire))
	result := &mcpgo.CallToolResult{IsError: wire.IsError}
	for _, content := range wire.Content {
		if content.Type == "text" {
			result.Content = append(result.Content, mcpgo.NewTextContent(content.Text))
		}
	}
	return result
}
