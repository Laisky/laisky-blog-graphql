package pageindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	glog "github.com/Laisky/zap"
	retry "github.com/avast/retry-go/v4"
	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
	"github.com/openai/openai-go/shared/constant"
	"golang.org/x/time/rate"
)

// LLM is the §2.6.5 contract used by the indexer and the search loop.
type LLM interface {
	Respond(ctx context.Context, req Request) (*Response, error)
	CountTokens(ctx context.Context, req Request) (int, error)
}

// InputItem mirrors a Responses-API input message.
type InputItem struct {
	Role    string
	Content string
}

// Request is a single LLM call.
type Request struct {
	Model        string
	Input        []InputItem
	Schema       json.RawMessage
	SchemaName   string
	MaxOutTokens int
	Temperature  float32
	PromptHash   [32]byte
}

// Response is the raw output and accounting from one Respond call.
type Response struct {
	Output   json.RawMessage
	Text     string
	Usage    Usage
	CacheHit bool
}

// Usage tracks token billing.
type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// LLMConfig configures the openaiLLM constructor.
type LLMConfig struct {
	APIKey    string
	BaseURL   string
	Model     string
	Limiter   *rate.Limiter
	Retry     RetrySettings
	Logger    logSDK.Logger
	Tokenizer Tokenizer
	// HTTPClient may override the http.Client used by the OpenAI SDK (tests inject httptest).
	HTTPClient *http.Client
}

// NewOpenAILLM builds an LLM that wraps openai-go's Responses.New.
func NewOpenAILLM(cfg LLMConfig) (LLM, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("api key is empty")
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-5.4-mini"
	}
	if cfg.Retry.MaxAttempts == 0 {
		cfg.Retry = RetrySettings{MaxAttempts: 10, InitialBackoff: 250 * time.Millisecond, MaxBackoff: 8 * time.Second}
	}
	tk := cfg.Tokenizer
	if tk == nil {
		t, err := NewTokenizer(cfg.Model)
		if err != nil {
			return nil, errors.Wrap(err, "default tokenizer")
		}
		tk = t
	}
	opts := []option.RequestOption{option.WithAPIKey(cfg.APIKey)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	// We drive retries with retry-go so the SDK does not double-retry on top of us.
	opts = append(opts, option.WithMaxRetries(0))
	client := openai.NewClient(opts...)
	keyHash := sha256.Sum256([]byte(cfg.APIKey))
	return &openaiLLM{
		client:     &client,
		model:      cfg.Model,
		limiter:    cfg.Limiter,
		retry:      cfg.Retry,
		log:        cfg.Logger,
		tok:        tk,
		keyHashHex: hex.EncodeToString(keyHash[:8]),
	}, nil
}

type openaiLLM struct {
	client     *openai.Client
	model      string
	limiter    *rate.Limiter
	retry      RetrySettings
	log        logSDK.Logger
	tok        Tokenizer
	keyHashHex string
}

// Respond performs a single Responses-API call, retrying transient errors.
func (l *openaiLLM) Respond(ctx context.Context, req Request) (*Response, error) {
	model := req.Model
	if model == "" {
		model = l.model
	}
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(model),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: buildInput(req.Input),
		},
	}
	if req.Temperature != 0 {
		params.Temperature = param.NewOpt(float64(req.Temperature))
	}
	if req.MaxOutTokens > 0 {
		params.MaxOutputTokens = param.NewOpt(int64(req.MaxOutTokens))
	}
	if len(req.Schema) > 0 {
		var schema map[string]any
		if err := json.Unmarshal(req.Schema, &schema); err != nil {
			return nil, errors.Wrap(err, "decode schema")
		}
		name := req.SchemaName
		if name == "" {
			name = "structured_output"
		}
		params.Text = responses.ResponseTextConfigParam{
			Format: responses.ResponseFormatTextConfigUnionParam{
				OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{
					Name:   name,
					Schema: schema,
					Strict: param.NewOpt(true),
					Type:   constant.JSONSchema("json_schema"),
				},
			},
		}
	}

	var resp *responses.Response
	start := time.Now()
	err := retry.Do(func() error {
		if l.limiter != nil {
			if waitErr := l.limiter.Wait(ctx); waitErr != nil {
				return waitErr
			}
		}
		out, callErr := l.client.Responses.New(ctx, params)
		if callErr != nil {
			return callErr
		}
		resp = out
		return nil
	},
		retry.Context(ctx),
		retry.Attempts(uint(l.retry.MaxAttempts)),
		retry.Delay(l.retry.InitialBackoff),
		retry.MaxDelay(l.retry.MaxBackoff),
		retry.DelayType(retry.BackOffDelay),
		retry.LastErrorOnly(true),
	)
	if err != nil {
		if l.log != nil {
			l.log.Warn("pageindex.llm.respond failed",
				glog.String("model", model),
				glog.String("api_key_hash", l.keyHashHex),
				glog.Error(err),
			)
		}
		return nil, errors.Wrap(err, "responses.new")
	}
	usage := Usage{
		InputTokens:  int(resp.Usage.InputTokens),
		OutputTokens: int(resp.Usage.OutputTokens),
		TotalTokens:  int(resp.Usage.TotalTokens),
	}
	text := resp.OutputText()
	out := &Response{Text: text, Usage: usage}
	if len(req.Schema) > 0 {
		out.Output = json.RawMessage(text)
	}
	if l.log != nil {
		l.log.Debug("pageindex.llm.respond",
			glog.String("model", model),
			glog.String("api_key_hash", l.keyHashHex),
			glog.String("prompt_hash", hex.EncodeToString(req.PromptHash[:4])),
			glog.Int("tokens_in", usage.InputTokens),
			glog.Int("tokens_out", usage.OutputTokens),
			glog.Duration("latency", time.Since(start)),
		)
	}
	return out, nil
}

// CountTokens estimates the prompt cost using the embedded BPE tokenizer.
func (l *openaiLLM) CountTokens(_ context.Context, req Request) (int, error) {
	total := 0
	for _, item := range req.Input {
		total += l.tok.Count(item.Content)
	}
	return total, nil
}

func buildInput(items []InputItem) responses.ResponseInputParam {
	out := make(responses.ResponseInputParam, 0, len(items))
	for _, it := range items {
		role := responses.EasyInputMessageRoleUser
		switch it.Role {
		case "system":
			role = responses.EasyInputMessageRoleSystem
		case "assistant":
			role = responses.EasyInputMessageRoleAssistant
		case "developer":
			role = responses.EasyInputMessageRoleDeveloper
		}
		out = append(out, responses.ResponseInputItemParamOfMessage(it.Content, role))
	}
	return out
}

// HashRequest derives the cache key for req. Schema is hashed verbatim.
func HashRequest(req Request) [32]byte {
	model := req.Model
	body, _ := json.Marshal(req.Input)
	params := struct {
		MaxOut      int     `json:"max_out_tokens"`
		Temperature float32 `json:"temperature"`
		SchemaName  string  `json:"schema_name,omitempty"`
	}{req.MaxOutTokens, req.Temperature, req.SchemaName}
	pbytes, _ := json.Marshal(params)
	return CacheKey(model, string(body), req.Schema, pbytes)
}

// Compile-time guards.
var (
	_ LLM = (*openaiLLM)(nil)
	_ LLM = (*StubLLM)(nil)
	_ LLM = (*NoopLLM)(nil)
)

// NoopLLM returns empty responses for callers that disable LLM access.
type NoopLLM struct{}

// Respond returns an empty Response.
func (NoopLLM) Respond(_ context.Context, _ Request) (*Response, error) {
	return &Response{Text: ""}, nil
}

// CountTokens returns 0.
func (NoopLLM) CountTokens(_ context.Context, _ Request) (int, error) { return 0, nil }

// StubLLM is a deterministic test LLM keyed by PromptHash.
type StubLLM struct {
	mu        sync.Mutex
	Responses map[[32]byte]*Response
	// Default is returned when no PromptHash matches.
	Default *Response
	// Calls counts every Respond invocation (test instrumentation).
	Calls int
	// InFlight tracks current concurrent calls (P12 verification).
	InFlight    int
	MaxInFlight int
	OnRespond   func(req Request)
}

// NewStubLLM constructs an empty StubLLM.
func NewStubLLM() *StubLLM { return &StubLLM{Responses: map[[32]byte]*Response{}} }

// Set registers a canned response for the supplied prompt hash.
func (s *StubLLM) Set(hash [32]byte, resp *Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Responses[hash] = resp
}

// SetDefault sets the fallback response.
func (s *StubLLM) SetDefault(resp *Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Default = resp
}

// Respond returns the canned response for req.PromptHash.
func (s *StubLLM) Respond(ctx context.Context, req Request) (*Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.Calls++
	s.InFlight++
	if s.InFlight > s.MaxInFlight {
		s.MaxInFlight = s.InFlight
	}
	cb := s.OnRespond
	resp, ok := s.Responses[req.PromptHash]
	if !ok {
		resp = s.Default
	}
	s.mu.Unlock()
	if cb != nil {
		cb(req)
	}
	defer func() {
		s.mu.Lock()
		s.InFlight--
		s.mu.Unlock()
	}()
	if resp == nil {
		return nil, errors.Errorf("stub: no canned response for prompt hash %s", hex.EncodeToString(req.PromptHash[:4]))
	}
	cp := *resp
	return &cp, nil
}

// CountTokens returns the byte length divided by 4.
func (s *StubLLM) CountTokens(_ context.Context, req Request) (int, error) {
	total := 0
	for _, it := range req.Input {
		total += len(it.Content) / 4
	}
	return total, nil
}

// CallCount returns the number of Respond invocations.
func (s *StubLLM) CallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Calls
}

// MaxConcurrent returns the high-water mark of simultaneous Respond invocations.
func (s *StubLLM) MaxConcurrent() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.MaxInFlight
}

// JSONResponse builds a Response from a JSON-marshalable value.
func JSONResponse(v any) *Response {
	body, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("pageindex: encode stub response: %v", err))
	}
	return &Response{Output: body, Text: string(body)}
}

// TextResponse builds a free-text Response.
func TextResponse(s string) *Response { return &Response{Text: s} }
