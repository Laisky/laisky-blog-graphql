package pageindex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/Laisky/zap/zaptest/observer"
	"github.com/stretchr/testify/require"
)

// responsesBody renders a Responses API body. usage is a JSON object or "" to
// omit usage; maxOut, when positive, is echoed as max_output_tokens.
func responsesBody(status, details, text, usage string, maxOut int) string {
	fields := []string{
		`"id":"resp_1"`, `"object":"response"`, `"created_at":0`, `"model":"gpt-5.4-mini"`,
		`"output":[{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":` + fmt.Sprintf("%q", text) + `,"annotations":[]}]}]`,
	}
	if status != "" {
		fields = append(fields, `"status":`+fmt.Sprintf("%q", status))
	}
	if details != "" {
		fields = append(fields, `"incomplete_details":`+details)
	}
	if usage != "" {
		fields = append(fields, `"usage":`+usage)
	}
	if maxOut > 0 {
		fields = append(fields, fmt.Sprintf(`"max_output_tokens":%d`, maxOut))
	}
	return "{" + strings.Join(fields, ",") + "}"
}

// usageJSON renders a usage object.
func usageJSON(in, out, total int) string {
	return fmt.Sprintf(`{"input_tokens":%d,"output_tokens":%d,"total_tokens":%d,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}`, in, out, total)
}

// TestResponsesProviderContract is the provider contract of the PageIndex client
// (third review of #96): the request every supported gateway receives, and how
// each answer shape seen from OpenAI and from OpenAI-compatible gateways
// (Laisky/one-api, new-api, LiteLLM, vLLM; docs/ref) is classified and charged.
// An answer is used and cached only when it is complete; one cut at its ceiling,
// or incomplete for any other reason, degrades its work; every answer is charged
// what the provider reported, or an estimate when it reported nothing.
func TestResponsesProviderContract(t *testing.T) {
	const granted = 256
	type outcome int
	const (
		used outcome = iota
		cut
		incomplete
		failed
	)
	for name, tc := range map[string]struct {
		status  int
		body    string
		want    outcome
		charged func(input, text int64) int64
		hits    int32
		warns   int
	}{
		"complete answer": {
			body: responsesBody("completed", "null", "a summary", usageJSON(30, 20, 50), granted), want: used,
			charged: func(int64, int64) int64 { return 50 },
		},
		"gateway omits status": {
			body: responsesBody("", "", "a summary", usageJSON(30, 20, 50), 0), want: used,
			charged: func(int64, int64) int64 { return 50 },
		},
		"cut at the ceiling (OpenAI, and chat-bridged length via one-api, new-api, LiteLLM)": {
			body: responsesBody("incomplete", `{"reason":"max_output_tokens"}`, "partial", usageJSON(30, granted, 30+granted), granted), want: cut,
			charged: func(int64, int64) int64 { return 30 + granted },
		},
		"cut with reasoning only and no text": {
			body: responsesBody("incomplete", `{"reason":"max_output_tokens"}`, "", usageJSON(30, granted, 30+granted), granted), want: cut,
			charged: func(int64, int64) int64 { return 30 + granted },
		},
		"ceiling used but not marked": {
			body: responsesBody("completed", "null", "partial", usageJSON(30, granted, 30+granted), granted), want: cut,
			charged: func(int64, int64) int64 { return 30 + granted },
		},
		"ceiling clamped by the server (vLLM echoes the clamp)": {
			body: responsesBody("completed", "null", "partial", usageJSON(30, 64, 94), 64), want: cut,
			charged: func(int64, int64) int64 { return 94 },
		},
		"incomplete without a reason (LiteLLM without finish_reason)": {
			body: responsesBody("incomplete", "null", "partial", usageJSON(30, 20, 50), granted), want: incomplete,
			charged: func(int64, int64) int64 { return 50 },
		},
		"incomplete by content filter": {
			body: responsesBody("incomplete", `{"reason":"content_filter"}`, "partial", usageJSON(30, 20, 50), granted), want: incomplete,
			charged: func(int64, int64) int64 { return 50 },
		},
		"failed status in a 200 answer": {
			body: responsesBody("failed", "null", "", usageJSON(30, 0, 30), granted), want: incomplete,
			charged: func(int64, int64) int64 { return 30 },
		},
		"ceiling ignored by an upstream": {
			body: responsesBody("completed", "null", "long answer", usageJSON(30, 900, 930), 0), want: cut,
			charged: func(int64, int64) int64 { return 930 }, warns: 1,
		},
		"usage without total (adaptors that recompute it)": {
			body: responsesBody("completed", "null", "a summary", usageJSON(30, 20, 0), granted), want: used,
			charged: func(int64, int64) int64 { return 50 },
		},
		"usage dropped by a proxy": {
			body: responsesBody("completed", "null", "a summary", "", 0), want: used,
			charged: func(input, text int64) int64 { return input + text },
		},
		"parameter rejected": {
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"Unsupported parameter: 'max_output_tokens'.","type":"invalid_request_error","param":"max_output_tokens","code":"unsupported_parameter"}}`,
			want:   failed, charged: func(input, _ int64) int64 { return input }, hits: 1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int32
			var sent map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				raw, err := io.ReadAll(r.Body)
				if err == nil {
					err = json.Unmarshal(raw, &sent)
				}
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(srv.Close)
			core, observed := observer.New(zap.WarnLevel)
			logger, err := logSDK.NewWithName("pageindex-contract", logSDK.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
			require.NoError(t, err)
			tok, err := NewTokenizer("gpt-5.4-mini")
			require.NoError(t, err)
			client, err := NewOpenAILLM(LLMConfig{APIKey: "sk-test", BaseURL: srv.URL, Retry: fastRetry(3), Logger: logger, Tokenizer: tok})
			require.NoError(t, err)
			gate := newLLMGate(tok, frozenSpend(LimitsSettings{}), LimitsSettings{MaxOutputTokensPerCall: granted})
			req := Request{Input: userInput("Summarize the section.")}
			budget := newOperationBudget(100_000, 10, unlimited, "t")

			resp, err := gate.call(context.Background(), client, req, budget)
			require.Equal(t, "gpt-5.4-mini", sent["model"])
			require.EqualValues(t, granted, sent["max_output_tokens"], "the reserved ceiling is sent as max_output_tokens")
			require.NotEmpty(t, sent["input"])
			switch tc.want {
			case used:
				require.NoError(t, err)
				require.False(t, resp.Truncated)
				require.False(t, resp.Incomplete)
			case cut:
				require.ErrorIs(t, err, errOutputCeiling, "an answer cut at its ceiling is never used")
			case incomplete:
				require.ErrorIs(t, err, errIncompleteAnswer, "an incomplete answer is never used")
				require.ErrorIs(t, err, ErrBudgetExceeded, "and degrades its work like a refused call")
			case failed:
				require.Error(t, err)
				require.NotErrorIs(t, err, ErrBudgetExceeded)
			}
			text := int64(0)
			if resp != nil {
				text = estimateTextTokens(tok, resp.Text)
			}
			require.Equal(t, tc.charged(estimateInputTokens(tok, req), text), 100_000-budget.Remaining(), "the request is charged what the provider reported")
			if tc.hits > 0 {
				require.Equal(t, tc.hits, hits.Load(), "a rejected parameter is not retried")
			}
			require.Equal(t, tc.warns, observed.FilterMessage("pageindex.llm.output_ceiling_ignored").Len())
		})
	}
}

// TestLiveResponsesProviderContract runs the same contract against a real
// Responses API endpoint. It is deliberate acceptance (docs/manual/fast_ci.md):
// it runs only when PAGEINDEX_CONTRACT_BASE_URL and PAGEINDEX_CONTRACT_API_KEY
// name an endpoint (PAGEINDEX_CONTRACT_MODEL defaults to gpt-5.4-mini), and it
// spends a few hundred tokens.
func TestLiveResponsesProviderContract(t *testing.T) {
	baseURL, apiKey := os.Getenv("PAGEINDEX_CONTRACT_BASE_URL"), os.Getenv("PAGEINDEX_CONTRACT_API_KEY")
	if baseURL == "" || apiKey == "" {
		t.Skip("set PAGEINDEX_CONTRACT_BASE_URL and PAGEINDEX_CONTRACT_API_KEY to run the live provider contract")
	}
	model := os.Getenv("PAGEINDEX_CONTRACT_MODEL")
	tok, err := NewTokenizer("gpt-5.4-mini")
	require.NoError(t, err)
	client, err := NewOpenAILLM(LLMConfig{APIKey: apiKey, BaseURL: baseURL, Model: model, Retry: fastRetry(2), Tokenizer: tok})
	require.NoError(t, err)
	ctx := context.Background()

	short, err := client.Respond(ctx, Request{Input: userInput("Reply with the single word: ok"), MaxOutTokens: minOutputTokens})
	require.NoError(t, err)
	t.Logf("short answer: usage %+v truncated=%v incomplete=%v", short.Usage, short.Truncated, short.Incomplete)
	require.False(t, short.Truncated)
	require.False(t, short.Incomplete)
	require.Positive(t, short.Usage.InputTokens, "the provider reports input usage")
	require.Positive(t, short.Usage.OutputTokens, "the provider reports output usage")
	require.Equal(t, short.Usage.InputTokens+short.Usage.OutputTokens, short.Usage.TotalTokens)
	require.LessOrEqual(t, short.Usage.OutputTokens, minOutputTokens)

	const ceiling = 16
	long, err := client.Respond(ctx, Request{Input: userInput("Count from 1 to 500, one number per line."), MaxOutTokens: ceiling})
	require.NoError(t, err)
	t.Logf("long answer: usage %+v truncated=%v incomplete=%v", long.Usage, long.Truncated, long.Incomplete)
	require.True(t, long.Truncated, "an answer longer than max_output_tokens is reported as cut")
	require.LessOrEqual(t, long.Usage.OutputTokens, ceiling, "the provider enforces max_output_tokens")
}

// TestOpenAILLMAcceptsTheGatewayOriginAsBaseURL reproduces a defect the live
// provider contract found: the deployed settings name the gateway origin
// (https://oneapi.laisky.com), as the other OpenAI-compatible settings of the
// service do, but the SDK appends "responses" to the base URL as given, so every
// PageIndex request reached the gateway's web UI and got an HTML page. An origin
// selects the /v1 API root, and a base URL with its own path is kept.
func TestOpenAILLMAcceptsTheGatewayOriginAsBaseURL(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/v1/responses" && r.URL.Path != "/gateway/openai/v1/responses" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, "<!doctype html><title>gateway console</title>")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, responsesBody("completed", "null", "ok", usageJSON(5, 1, 6), 0))
	}))
	t.Cleanup(srv.Close)
	for base, path := range map[string]string{
		srv.URL:                                "/v1/responses",
		srv.URL + "/":                          "/v1/responses",
		srv.URL + "/v1":                        "/v1/responses",
		srv.URL + "/v1/":                       "/v1/responses",
		srv.URL + "/v1/responses":              "/v1/responses",
		" " + srv.URL + "/gateway/openai/v1/ ": "/gateway/openai/v1/responses",
		srv.URL + "/gateway/openai/v1/responses/": "/gateway/openai/v1/responses",
	} {
		paths = nil
		llm, err := NewOpenAILLM(LLMConfig{APIKey: "sk-test", BaseURL: base, Retry: fastRetry(1)})
		require.NoError(t, err)
		resp, err := llm.Respond(context.Background(), Request{Input: userInput("hi"), MaxOutTokens: minOutputTokens})
		require.NoError(t, err, "base URL %q", base)
		require.Equal(t, "ok", resp.Text)
		require.Equal(t, []string{path}, paths, "base URL %q", base)
	}
}
