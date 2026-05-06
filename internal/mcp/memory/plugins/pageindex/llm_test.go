package pageindex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const okResponseJSON = `{
  "id": "resp_1",
  "object": "response",
  "created_at": 0,
  "model": "gpt-5.4-mini",
  "status": "completed",
  "output": [
    {"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "{\"answer\":\"yes\"}"}]}
  ],
  "usage": {"input_tokens": 5, "output_tokens": 2, "total_tokens": 7, "input_tokens_details": {"cached_tokens": 0}, "output_tokens_details": {"reasoning_tokens": 0}},
  "parallel_tool_calls": false,
  "temperature": 0,
  "tool_choice": "auto",
  "tools": [],
  "top_p": 1
}`

func TestOpenAILLMHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okResponseJSON))
	}))
	defer srv.Close()
	llm, err := NewOpenAILLM(LLMConfig{APIKey: "sk-test", BaseURL: srv.URL, Model: "gpt-5.4-mini", Retry: RetrySettings{MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := llm.Respond(context.Background(), Request{Input: []InputItem{{Role: "user", Content: "Hi"}}})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if resp.Text == "" {
		t.Fatalf("expected text, got empty")
	}
	if resp.Usage.TotalTokens != 7 {
		t.Fatalf("usage total expected 7, got %d", resp.Usage.TotalTokens)
	}
}

func TestOpenAILLMRetriesOn5xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"boom","type":"server_error"}}`))
			return
		}
		_, _ = w.Write([]byte(okResponseJSON))
	}))
	defer srv.Close()
	llm, err := NewOpenAILLM(LLMConfig{APIKey: "sk-test", BaseURL: srv.URL, Model: "gpt-5.4-mini", Retry: RetrySettings{MaxAttempts: 5, InitialBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := llm.Respond(context.Background(), Request{Input: []InputItem{{Role: "user", Content: "Hi"}}}); err != nil {
		t.Fatalf("respond after retry: %v", err)
	}
	if calls.Load() < 3 {
		t.Fatalf("expected at least 3 attempts, got %d", calls.Load())
	}
}

// TestOpenAILLMRetryAfterHonored (P11) — a 429 response with Retry-After must
// be retried, and after exhausting MaxAttempts the call surfaces an error.
// We verify the retry policy by checking the call count and the eventual
// error path; observing the exact Retry-After delay is brittle in unit tests.
func TestOpenAILLMRetryAfterHonored(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Always return 429 with a small Retry-After. The avast/retry-go layer
		// must keep retrying up to MaxAttempts before surfacing failure.
		w.Header().Set("Retry-After", "0")
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate","type":"rate_limit"}}`))
	}))
	defer srv.Close()
	llm, err := NewOpenAILLM(LLMConfig{
		APIKey: "sk-test", BaseURL: srv.URL, Model: "gpt-5.4-mini",
		Retry: RetrySettings{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, respondErr := llm.Respond(context.Background(), Request{Input: []InputItem{{Role: "user", Content: "Hi"}}})
	if respondErr == nil {
		t.Fatalf("persistent 429 should bubble up after MaxAttempts; got nil error")
	}
	if got := calls.Load(); got < 2 {
		t.Fatalf("expected retry-go to retry at least twice on 429 with Retry-After, got %d attempts", got)
	}
}

func TestStubLLMPlumbing(t *testing.T) {
	stub := NewStubLLM()
	stub.SetDefault(JSONResponse(map[string]string{"answer": "yes"}))
	resp, err := stub.Respond(context.Background(), Request{Input: []InputItem{{Role: "user", Content: "x"}}, PromptHash: HashRequest(Request{Input: []InputItem{{Role: "user", Content: "x"}}})})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text == "" {
		t.Fatal("stub should return canned text")
	}
	if stub.CallCount() != 1 {
		t.Fatalf("expected 1 call, got %d", stub.CallCount())
	}
}
