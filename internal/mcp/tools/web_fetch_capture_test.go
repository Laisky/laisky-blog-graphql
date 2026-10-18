package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/Laisky/zap/zaptest/observer"
	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	"github.com/Laisky/laisky-blog-graphql/library/crawlercapture"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	appLog "github.com/Laisky/laisky-blog-graphql/library/log"
	"github.com/Laisky/laisky-blog-graphql/library/search"
)

// TestCrawlerFailureJournalCallerRecords requires the actual tool failure to
// produce an authoritative local record before returning the unchanged error.
func TestCrawlerFailureJournalCallerRecords(t *testing.T) {
	directory := privateCallerJournalDirectory(t)
	t.Setenv("CRAWLER_FAILURE_LOG_DIR", directory)
	logger, err := logSDK.NewWithName("capture-test", logSDK.LevelInfo,
		zap.WrapCore(func(zapcore.Core) zapcore.Core { return zapcore.NewNopCore() }))
	require.NoError(t, err)
	tool, err := NewWebFetchTool(&rlibs.DB{}, logger, func(context.Context) string { return "synthetic-auth-not-crawl-input" },
		func(context.Context, string, oneapi.Price, string) error { return nil },
		func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) {
			return nil, errors.New("synthetic worker failure")
		})
	require.NoError(t, err)
	result, err := tool.Handle(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{"url": "  https://1.1.1.1/original?query=synthetic-input  "},
	}})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Equal(t, "fetch failed: fetch_failed", result.Content[0].(mcp.TextContent).Text)
	var records []string
	require.NoError(t, filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			records = append(records, path)
		}
		return nil
	}))
	require.Len(t, records, 1, "a failed accepted crawl must retain its complete original input in the private journal")
}

// TestCrawlerFailureJournalOriginalDeclaredInputs preserves only actual declared
// crawl inputs, with explicit missing/null/default semantics and task binding.
func TestCrawlerFailureJournalOriginalDeclaredInputs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		present   bool
		value     any
		effective bool
	}{
		{"absent", false, nil, true}, {"null", true, nil, true},
		{"false", true, false, false}, {"true", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := privateCallerJournalDirectory(t)
			t.Setenv("CRAWLER_FAILURE_LOG_DIR", directory)
			original := " \thttps://1.1.1.1/Unicode-\u4f60\u597d?literal=synthetic-input-canary#fragment \n"
			args := map[string]any{"url": original, "unknown_api_key": "synthetic-unknown-auth-canary"}
			if tc.present {
				args["output_markdown"] = tc.value
			}
			core, observed := observer.New(zapcore.DebugLevel)
			logger, err := logSDK.NewWithName("capture-test", logSDK.LevelInfo,
				zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
			require.NoError(t, err)
			tool, err := NewWebFetchTool(&rlibs.DB{}, logger,
				func(context.Context) string { return "synthetic-provider-auth-canary" },
				func(context.Context, string, oneapi.Price, string) error { return nil },
				func(_ context.Context, _ *rlibs.DB, target, key string, markdown bool) ([]byte, error) {
					require.Equal(t, strings.TrimSpace(original), target)
					require.Equal(t, tc.effective, markdown)
					require.Equal(t, "synthetic-provider-auth-canary", key)
					return nil, &search.FetchFailureError{Diagnostic: search.FetchFailureDiagnostic{
						ErrorCode: "crawler_result_failed", Stage: "result",
						TaskID: "0199c118-4229-7000-8000-000000000002",
					}, Cause: errors.New("synthetic worker result lookup failed")}
				})
			require.NoError(t, err)
			req := mcp.CallToolRequest{Header: http.Header{"Authorization": {"Bearer synthetic-header-auth-canary"}},
				Params: mcp.CallToolParams{Arguments: args}}
			result, err := tool.Handle(context.Background(), req)
			require.NoError(t, err)
			require.True(t, result.IsError)
			raw, record := readCallerCapture(t, directory)
			require.NotContains(t, string(raw), "synthetic-provider-auth-canary")
			payloads := captureDecodedPayloads(t, record)
			require.NotContains(t, payloads, "synthetic-provider-auth-canary")
			require.NotContains(t, payloads, "synthetic-header-auth-canary")
			require.NotContains(t, payloads, "synthetic-unknown-auth-canary")
			require.NotContains(t, string(raw), "synthetic-header-auth-canary")
			require.NotContains(t, string(raw), "synthetic-unknown-auth-canary")
			request := record["request"].(map[string]any)
			require.Equal(t, []byte(original), captureBlobBytes(t, request["original_url"]))
			require.Equal(t, []byte(strings.TrimSpace(original)), captureBlobBytes(t, request["submitted_url"]))
			require.Equal(t, tc.present, request["output_markdown_declared"])
			require.Equal(t, tc.effective, request["output_markdown"])
			if tc.present {
				expected, err := json.Marshal(tc.value)
				require.NoError(t, err)
				require.JSONEq(t, string(expected), string(captureBlobBytes(t, request["original_output_markdown"])))
			}
			if exportDir := os.Getenv("CRAWLER_CAPTURE_TEST_FIXTURE_DIR"); exportDir != "" {
				require.NoError(t, os.MkdirAll(exportDir, 0700))
				require.NoError(t, os.WriteFile(filepath.Join(exportDir, "caller-"+tc.name+".json"), raw, 0600))
			}
			correlation := record["correlation"].(map[string]any)
			require.Equal(t, "0199c118-4229-7000-8000-000000000002", correlation["task_id"])
			require.NotEmpty(t, correlation["request_id"])
			source := record["source"].(map[string]any)
			require.Equal(t, "laisky-blog-graphql", source["service"])
			require.NotEmpty(t, source["hostname"])
			require.NotEmpty(t, source["process_run_id"])
			entries := observed.FilterMessage("web_fetch failed").All()
			require.Len(t, entries, 1)
			require.Equal(t, "complete", entries[0].ContextMap()["failure_capture_status"])
			require.NotEmpty(t, entries[0].ContextMap()["failure_capture_path"])
			require.NotEmpty(t, entries[0].ContextMap()["failure_capture_sha256"])
		})
	}
}

// readCallerCapture reads the one actual journal record written by the tool.
func readCallerCapture(t *testing.T, directory string) ([]byte, map[string]any) {
	t.Helper()
	var paths []string
	require.NoError(t, filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			paths = append(paths, path)
		}
		return nil
	}))
	require.Len(t, paths, 1)
	_, err := crawlercapture.Read(paths[0])
	require.NoError(t, err)
	raw, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	var record map[string]any
	require.NoError(t, json.Unmarshal(raw, &record))
	require.True(t, record["complete"].(bool))
	return raw, record
}

// captureBlobBytes verifies the declared length and reverses the byte codec.
func captureBlobBytes(t *testing.T, value any) []byte {
	t.Helper()
	blob := value.(map[string]any)
	data, err := base64.StdEncoding.DecodeString(blob["base64"].(string))
	require.NoError(t, err)
	require.Equal(t, float64(len(data)), blob["byte_length"])
	return bytes.Clone(data)
}

// TestCrawlerFailureJournalBeforeQueueAndWriteFailure retains rejected declared
// inputs, while a failed journal write remains visibly incomplete.
func TestCrawlerFailureJournalBeforeQueueAndWriteFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		broken bool
		url    string
	}{
		{"admission", false, "https://127.0.0.1/private"},
		{"large rejected declared input", false, "https://1.1.1.1/" + strings.Repeat("synthetic-large-", 1000)},
		{"invalid UTF8 rejected declared input", false, "https://1.1.1.1/" + string([]byte{0xff, 0xfe})},
		{"unwritable journal", true, "https://1.1.1.1/declared-input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := privateCallerJournalDirectory(t)
			if tc.broken {
				directory = filepath.Join(directory, "regular-file")
				require.NoError(t, os.WriteFile(directory, []byte("synthetic occupied path"), 0600))
			}
			t.Setenv("CRAWLER_FAILURE_LOG_DIR", directory)
			core, observed := observer.New(zapcore.DebugLevel)
			logger, err := logSDK.NewWithName("capture-test", logSDK.LevelInfo,
				zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
			require.NoError(t, err)
			fetches, billings := 0, 0
			tool, err := NewWebFetchTool(&rlibs.DB{}, logger, func(context.Context) string { return "synthetic-auth-excluded" },
				func(context.Context, string, oneapi.Price, string) error { billings++; return nil },
				func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) {
					fetches++
					return nil, errors.New("synthetic submit failure")
				})
			require.NoError(t, err)
			result, err := tool.Handle(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"url": tc.url}}})
			require.NoError(t, err)
			require.True(t, result.IsError)
			if tc.broken {
				require.Equal(t, 1, fetches)
				require.Equal(t, 1, billings)
				entries := observed.FilterMessage("web_fetch failed").All()
				require.Len(t, entries, 1)
				fields := entries[0].ContextMap()
				require.Equal(t, "write_failed", fields["failure_capture_status"])
				require.NotContains(t, fields, "failure_capture_sha256")
				require.NotContains(t, fields, "failure_capture_path")
			} else {
				require.Zero(t, fetches)
				require.Zero(t, billings)
				_, record := readCallerCapture(t, directory)
				request := record["request"].(map[string]any)
				require.Equal(t, []byte(tc.url), captureBlobBytes(t, request["original_url"]))
				correlation := record["correlation"].(map[string]any)
				require.Empty(t, correlation["task_id"])
				require.NotEmpty(t, correlation["request_id"])
			}
		})
	}
}

// TestCrawlerFailureJournalSuccessWritesNothing preserves the success path.
func TestCrawlerFailureJournalSuccessWritesNothing(t *testing.T) {
	directory := privateCallerJournalDirectory(t)
	t.Setenv("CRAWLER_FAILURE_LOG_DIR", directory)
	logger, err := logSDK.NewWithName("capture-test", logSDK.LevelInfo,
		zap.WrapCore(func(zapcore.Core) zapcore.Core { return zapcore.NewNopCore() }))
	require.NoError(t, err)
	tool, err := NewWebFetchTool(&rlibs.DB{}, logger, func(context.Context) string { return "synthetic-key" },
		func(context.Context, string, oneapi.Price, string) error { return nil },
		func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) {
			return []byte("synthetic success"), nil
		})
	require.NoError(t, err)
	result, err := tool.Handle(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"url": "https://1.1.1.1/doc"}}})
	require.NoError(t, err)
	require.False(t, result.IsError)
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// captureDecodedPayloads examines all reversible blobs for excluded synthetic authorization values.
func captureDecodedPayloads(t *testing.T, value any) string {
	t.Helper()
	var payloads []byte
	var walk func(any)
	walk = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if state, ok := value["state"].(string); ok && state == "captured" {
				if text, ok := value["base64"].(string); ok {
					data, err := base64.StdEncoding.DecodeString(text)
					require.NoError(t, err)
					payloads = append(payloads, data...)
				}
			}
			for _, child := range value {
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(value)
	return string(payloads)
}

// TestCrawlerFailureJournalDefaultAlertReferencesCompleteLog exercises the real
// default SDK HTTP hook: it references the saved journal without carrying payload.
func TestCrawlerFailureJournalDefaultAlertReferencesCompleteLog(t *testing.T) {
	directory := privateCallerJournalDirectory(t)
	t.Setenv("CRAWLER_FAILURE_LOG_DIR", directory)
	messages := make(chan string, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Variables map[string]json.RawMessage `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Error(err)
			return
		}
		var message string
		if err := json.Unmarshal(envelope.Variables["msg"], &message); err != nil {
			t.Error(err)
			return
		}
		messages <- message
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"TelegramMonitorAlert":{"name":"synthetic"}}}`))
	}))
	t.Cleanup(peer.Close)
	alert, err := logSDK.NewAlert(context.Background(), peer.URL, logSDK.WithAlertType("synthetic"),
		logSDK.WithAlertToken("synthetic-alert-token"))
	require.NoError(t, err)
	t.Cleanup(alert.Close)
	core, _ := observer.New(zapcore.InfoLevel)
	logger, err := logSDK.NewWithName("graphql.laisky-graphql.mcp.web_fetch", logSDK.LevelInfo,
		zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }),
		zap.HooksWithFields(appLog.WebDiagnosticAlertHook(alert.GetZapHook())))
	require.NoError(t, err)
	tool, err := NewWebFetchTool(&rlibs.DB{}, logger, func(context.Context) string { return "synthetic-api-key" },
		func(context.Context, string, oneapi.Price, string) error { return nil },
		func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) {
			return nil, &search.FetchFailureError{Diagnostic: search.FetchFailureDiagnostic{
				ErrorCode: "crawler_failed", Stage: "render", TaskID: "0199c118-4229-7000-8000-000000000002",
				WorkerStage: "conversion", WorkerCategory: "empty_markdown",
			}, Cause: errors.New("synthetic conversion failure")}
		})
	require.NoError(t, err)
	result, err := tool.Handle(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{"url": "https://1.1.1.1/original-private-canary?q=synthetic"},
	}})
	require.NoError(t, err)
	require.True(t, result.IsError)
	raw, record := readCallerCapture(t, directory)
	failure := record["failure"].(map[string]any)
	require.Equal(t, "worker_result", failure["stage"])
	require.Equal(t, "conversion", failure["worker_stage"])
	require.Equal(t, "empty_markdown", failure["worker_code"])
	sum := sha256.Sum256(raw)
	select {
	case message := <-messages:
		require.Contains(t, message, `"failure_capture_status":"complete"`)
		require.Contains(t, message, `"failure_capture_sha256":"`+hex.EncodeToString(sum[:])+`"`)
		require.Contains(t, message, `"failure_capture_path":`)
		require.Contains(t, message, `"origin_service":"laisky-blog-graphql"`)
		require.NotContains(t, message, "original-private-canary")
		require.NotContains(t, message, "synthetic-api-key")
	case <-time.After(3 * time.Second):
		t.Fatal("default SDK did not deliver the bounded saved-log reference")
	}
}

// TestCrawlerFailureJournalMissingAuthorizationWritesNothing preserves the
// authorization boundary: unadmitted unauthenticated input is not persisted.
func TestCrawlerFailureJournalMissingAuthorizationWritesNothing(t *testing.T) {
	directory := privateCallerJournalDirectory(t)
	t.Setenv("CRAWLER_FAILURE_LOG_DIR", directory)
	logger, err := logSDK.NewWithName("capture-test", logSDK.LevelInfo,
		zap.WrapCore(func(zapcore.Core) zapcore.Core { return zapcore.NewNopCore() }))
	require.NoError(t, err)
	tool, err := NewWebFetchTool(&rlibs.DB{}, logger, func(context.Context) string { return "" },
		func(context.Context, string, oneapi.Price, string) error { t.Fatal("unexpected billing"); return nil },
		func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) {
			t.Fatal("unexpected fetch")
			return nil, nil
		})
	require.NoError(t, err)
	result, err := tool.Handle(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{"url": "https://1.1.1.1/unadmitted"},
	}})
	require.NoError(t, err)
	require.True(t, result.IsError)
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// privateCallerJournalDirectory makes only this test-owned leaf explicitly
// private; t.TempDir's leaf mode is not assumed under the active Go runtime.
func privateCallerJournalDirectory(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	require.NoError(t, os.Chmod(directory, 0700))
	return directory
}

// TestCrawlerFailureJournalLongPathReferenceOrder exercises the actual SDK HTTP
// serializer with a long exact path. Recovery identity survives a 3000-rune view.
func TestCrawlerFailureJournalLongPathReferenceOrder(t *testing.T) {
	messages := make(chan string, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Variables map[string]json.RawMessage `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Error(err)
			return
		}
		var message string
		if err := json.Unmarshal(envelope.Variables["msg"], &message); err != nil {
			t.Error(err)
			return
		}
		messages <- message
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"TelegramMonitorAlert":{"name":"synthetic"}}}`))
	}))
	t.Cleanup(peer.Close)
	alert, err := logSDK.NewAlert(context.Background(), peer.URL, logSDK.WithAlertType("synthetic"),
		logSDK.WithAlertToken("synthetic-alert-token"))
	require.NoError(t, err)
	t.Cleanup(alert.Close)
	reference := crawlercapture.Reference{
		Status: "complete", CaptureID: strings.Repeat("1", 32), SHA256: strings.Repeat("a", 64), Bytes: 12345,
		Path:   "/synthetic/private/" + strings.Repeat("p", 3500) + "/capture.json",
		Source: crawlercapture.Source{Service: "laisky-blog-graphql", Hostname: "synthetic-instance", ProcessRunID: "synthetic-process-run"},
	}
	fields := captureReferenceFields(reference, nil, zap.String("stage", "worker_result"))
	hook := appLog.WebDiagnosticAlertHook(alert.GetZapHook())
	require.NoError(t, hook(zapcore.Entry{LoggerName: "graphql.laisky-graphql.mcp.web_fetch",
		Level: zapcore.ErrorLevel, Message: "web_fetch failed", Time: time.Now()}, fields))
	select {
	case message := <-messages:
		runes := []rune(message)
		require.Greater(t, len(runes), 3000)
		preview := string(runes[:3000])
		for _, expected := range []string{
			`"origin_service":"laisky-blog-graphql"`, `"origin_hostname":"synthetic-instance"`,
			`"origin_process_run_id":"synthetic-process-run"`, `"failure_capture_status":"complete"`,
			`"failure_capture_id":"` + reference.CaptureID + `"`,
			`"failure_capture_sha256":"` + reference.SHA256 + `"`, `"failure_capture_bytes":12345`,
		} {
			require.Contains(t, preview, expected)
		}
		encoded, err := json.Marshal(reference.Path)
		require.NoError(t, err)
		require.Contains(t, message, `"failure_capture_path":`+string(encoded), "full path is preserved in the complete SDK message")
	case <-time.After(3 * time.Second):
		t.Fatal("long reference missing from actual default SDK HTTP transport")
	}
}
