package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	appLog "github.com/Laisky/laisky-blog-graphql/library/log"
	"github.com/Laisky/laisky-blog-graphql/library/search"
)

const adoptionRequestID = "0199c118-4229-7000-8000-000000000001"
const adoptionTaskID = "0199c118-4229-7000-8000-000000000002"

// adoptionCallerOutcome keeps only synthetic execution and ordinary logging observations.
type adoptionCallerOutcome struct {
	result      *mcp.CallToolResult
	billings    int
	fetches     int
	local       []byte
	errorOutput string
}

// runAdoptionCaller couples the actual caller and scoped SDK core to an explicit loopback receiver.
// Billing, Redis access and crawling are replaced with bounded in-process dependencies only.
func runAdoptionCaller(t *testing.T, endpoint string, args map[string]any, authorized, success bool) adoptionCallerOutcome {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	alert, err := logSDK.NewAlert(ctx, endpoint, logSDK.WithAlertType(receiverAlertType), logSDK.WithAlertToken(receiverAlertToken),
		logSDK.WithAlertMaxMessageBytes(1<<20))
	require.NoError(t, err)
	t.Cleanup(func() {
		cancel()
		alert.Close()
		select {
		case <-alert.Done():
		case <-time.After(3 * time.Second):
			t.Error("synthetic adoption sender did not stop")
		}
	})
	var local, errorOutput bytes.Buffer
	localCore := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&local), zap.DebugLevel)
	logger, err := logSDK.NewWithName("graphql.laisky-graphql.mcp.web_fetch", logSDK.LevelDebug,
		zap.WrapCore(func(zapcore.Core) zapcore.Core {
			return zapcore.NewTee(localCore, appLog.WebDiagnosticAlertCore(alert))
		}), zap.ErrorOutput(zapcore.AddSync(&errorOutput)))
	require.NoError(t, err)
	outcome := adoptionCallerOutcome{}
	tool, err := tools.NewWebFetchTool(&rlibs.DB{}, logger, func(context.Context) string {
		if authorized {
			return "SYNTHETIC_PROVIDER_AUTH_NOT_SELECTED"
		}
		return ""
	}, func(context.Context, string, oneapi.Price, string) error {
		outcome.billings++
		return nil
	}, func(_ context.Context, _ *rlibs.DB, target, key string, markdown bool) ([]byte, error) {
		outcome.fetches++
		require.Equal(t, strings.TrimSpace(args["url"].(string)), target)
		require.Equal(t, "SYNTHETIC_PROVIDER_AUTH_NOT_SELECTED", key)
		expectedMarkdown := true
		if declared, ok := args["output_markdown"].(bool); ok {
			expectedMarkdown = declared
		}
		require.Equal(t, expectedMarkdown, markdown)
		if success {
			return []byte("synthetic successful result"), nil
		}
		return nil, &search.FetchFailureError{Diagnostic: search.FetchFailureDiagnostic{
			ErrorCode: "crawler_failed", Stage: "render", TaskID: adoptionTaskID,
			WorkerStage: "conversion", WorkerCategory: "empty_markdown",
		}, Cause: errors.New("synthetic conversion failure")}
	})
	require.NoError(t, err)
	requestContext := context.WithValue(context.Background(), ctxkeys.RequestID, adoptionRequestID)
	outcome.result, err = tool.Handle(requestContext, mcp.CallToolRequest{
		Header: http.Header{"Authorization": {"Bearer SYNTHETIC_HEADER_AUTH_NOT_SELECTED"}},
		Params: mcp.CallToolParams{Arguments: args},
	})
	require.NoError(t, err)
	outcome.local = bytes.Clone(local.Bytes())
	outcome.errorOutput = errorOutput.String()
	return outcome
}

// adoptionDeliveryPayload reads the entire actual Bot API text or document without normalizing bytes.
func adoptionDeliveryPayload(t *testing.T, harness *receiverHarness) string {
	t.Helper()
	require.Eventually(t, func() bool {
		deliveries, _ := harness.bot.snapshot()
		return len(deliveries) == 1
	}, 3*time.Second, 10*time.Millisecond)
	deliveries, parseErrors := harness.bot.snapshot()
	require.Empty(t, parseErrors)
	require.Len(t, deliveries, 1)
	delivery := deliveries[0]
	require.Equal(t, "101", delivery.UID)
	require.Empty(t, delivery.Fields["parse_mode"])
	require.Empty(t, delivery.Fields["caption"])
	if delivery.Method == "sendDocument" {
		require.Equal(t, "alert.txt", delivery.Filename)
		return string(delivery.Data)
	}
	require.Equal(t, "sendMessage", delivery.Method)
	return delivery.Text
}

// adoptionDeliveredFields decodes the actual SDK field suffix with exact JSON integers.
func adoptionDeliveredFields(t *testing.T, payload string) map[string]any {
	t.Helper()
	offset := strings.LastIndex(payload, "\n{")
	require.GreaterOrEqual(t, offset, 0)
	decoder := json.NewDecoder(strings.NewReader(payload[offset+1:]))
	decoder.UseNumber()
	var fields map[string]any
	require.NoError(t, decoder.Decode(&fields))
	return fields
}

// adoptionLocalFailure finds the actual caller's selected original fields in its local sink.
func adoptionLocalFailure(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var failure map[string]any
	for {
		var event map[string]any
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		if event["msg"] == "web_fetch failed" {
			require.Nil(t, failure, "one business failure must author only one failure event")
			failure = event
		}
	}
	require.NotNil(t, failure)
	return failure
}

// TestAlertAdoptionActualCallerCompleteContent crosses caller, SDK, GraphQL, and actual Bot HTTP.
func TestAlertAdoptionActualCallerCompleteContent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared bool
		option   any
		padding  string
	}{
		{name: "absent"},
		{name: "null", declared: true},
		{name: "false", declared: true, option: false},
		{name: "true", declared: true, option: true},
		{name: "large chosen original", padding: strings.Repeat(" \t", 10*1024)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			harness := newReceiverHarness(t, []int{101}, nil, true)
			original := " \t" + tc.padding + "https://1.1.1.1/Unicode-你好?literal=chosen-input#fragment\n"
			args := map[string]any{"url": original, "unknown_auth": map[string]any{"secret": "SYNTHETIC_UNKNOWN_ARGUMENT_NOT_SELECTED"}}
			if tc.declared {
				args["output_markdown"] = tc.option
			}
			outcome := runAdoptionCaller(t, harness.server.URL, args, true, false)
			require.True(t, outcome.result.IsError)
			require.Equal(t, "fetch failed: crawler_failed", outcome.result.Content[0].(mcp.TextContent).Text)
			require.Equal(t, 1, outcome.billings)
			require.Equal(t, 1, outcome.fetches)
			payload := adoptionDeliveryPayload(t, harness)
			require.True(t, strings.HasPrefix(payload, receiverAlertType+" >>>>>>>>>>>>>>>>>> \n"))
			fields := adoptionDeliveredFields(t, payload)
			require.Equal(t, original, fields["original_url"])
			require.Equal(t, strings.TrimSpace(original), fields["submitted_url"])
			require.Equal(t, "utf8", fields["original_url_encoding"])
			require.Equal(t, adoptionRequestID, fields["request_id"])
			require.Equal(t, adoptionTaskID, fields["task_id"])
			require.Equal(t, "laisky-blog-graphql", fields["origin_service"])
			require.Equal(t, "worker_result", fields["caller_phase"])
			require.Equal(t, tc.declared, fields["output_markdown_declared"])
			require.Contains(t, fields, "declared_output_markdown")
			require.Equal(t, tc.option, fields["declared_output_markdown"])
			require.Equal(t, tc.option != false, fields["output_markdown"])
			require.Equal(t, original, adoptionLocalFailure(t, outcome.local)["original_url"])
			for _, notSelected := range []string{"SYNTHETIC_PROVIDER_AUTH_NOT_SELECTED", "SYNTHETIC_HEADER_AUTH_NOT_SELECTED", "SYNTHETIC_UNKNOWN_ARGUMENT_NOT_SELECTED"} {
				require.NotContains(t, payload, notSelected)
				require.NotContains(t, string(outcome.local), notSelected)
			}
			require.Empty(t, outcome.errorOutput)
		})
	}
}

// TestAlertAdoptionOversizeKeepsBusinessAndLocalInput rejects only remote admission without truncation.
func TestAlertAdoptionOversizeKeepsBusinessAndLocalInput(t *testing.T) {
	harness := newReceiverHarness(t, []int{101}, nil, true)
	original := strings.Repeat(" \t", (1<<20)/2) + "https://1.1.1.1/doc\n"
	outcome := runAdoptionCaller(t, harness.server.URL, map[string]any{"url": original}, true, false)
	require.True(t, outcome.result.IsError)
	require.Equal(t, 1, outcome.billings)
	require.Equal(t, 1, outcome.fetches)
	require.Equal(t, original, adoptionLocalFailure(t, outcome.local)["original_url"])
	require.Contains(t, outcome.errorOutput, logSDK.ErrAlertMessageTooLarge.Error())
	deliveries, parseErrors := harness.bot.snapshot()
	require.Empty(t, deliveries, "oversized entries must never send a clipped prefix")
	require.Empty(t, parseErrors)
}

// TestAlertAdoptionSuccessAndUnauthorizedRemainQuiet preserves successful and unauthenticated paths.
func TestAlertAdoptionSuccessAndUnauthorizedRemainQuiet(t *testing.T) {
	for _, authorized := range []bool{false, true} {
		name := "unauthorized"
		if authorized {
			name = "success"
		}
		t.Run(name, func(t *testing.T) {
			harness := newReceiverHarness(t, []int{101}, nil, true)
			outcome := runAdoptionCaller(t, harness.server.URL, map[string]any{"url": "https://1.1.1.1/doc"}, authorized, true)
			require.Equal(t, !authorized, outcome.result.IsError)
			expected := 0
			if authorized {
				expected = 1
			}
			require.Equal(t, expected, outcome.billings)
			require.Equal(t, expected, outcome.fetches)
			deliveries, parseErrors := harness.bot.snapshot()
			require.Empty(t, deliveries)
			require.Empty(t, parseErrors)
			require.NotContains(t, string(outcome.local), "original_url")
		})
	}
}
