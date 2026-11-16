package pageindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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
	"github.com/openai/openai-go/shared/constant"
	"golang.org/x/time/rate"

	"github.com/Laisky/laisky-blog-graphql/library/httpclient"
)

// LLM is the §2.6.5 contract used by the indexer and the search loop.
type LLM interface {
	Respond(ctx context.Context, req Request) (*Response, error)
	CountTokens(ctx context.Context, req Request) (int, error)
}

// InputItem mirrors a Responses-API input message.
type InputItem struct {
	Role    string `json:"Role"`
	Content string `json:"Content"`
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
	// LongOutput marks a request whose answer re-emits a table of contents; the
	// llmGate grants it limits.max_toc_output_tokens_per_call instead of
	// limits.max_output_tokens_per_call. The gate sets MaxOutTokens after the
	// cache key is computed, so neither changes the key of an ordinary request:
	// a ceiling never changes a complete answer, and a truncated one is never cached.
	LongOutput bool
	// AdmitRetry, when set, is called before every retry of the call (attempt 2
	// onwards) with the error the previous request failed with. An error stops
	// retrying and is returned, so each upstream request is admitted by the
	// budgets before it is sent. It is not part of the cache key.
	AdmitRetry func(attempt int, prev error) error
}

// errRequestNotSent marks a failure that happened before the request left the
// process (the client-side rate limiter), so the request costs nothing upstream.
var errRequestNotSent = errors.New("llm request not sent")

// errDeadlineAhead is the errRequestNotSent of a request the client-side rate
// limiter could not send before its context's deadline: it anticipates the
// deadline, so the work it was for degrades like the deadline itself.
var errDeadlineAhead = errors.Wrap(errRequestNotSent, "llm request would outlive its deadline")

// DefaultLLMRetryAttempts is the default number of upstream requests one call may
// make, including the first (settings indexer.retry.max_attempts).
const DefaultLLMRetryAttempts = 3

// isRetryableLLMError reports whether a failed upstream request may be retried:
// timeouts, connection failures, 408, 409, 429 and 5xx. Any other provider
// response (an invalid request, authentication, a missing model) fails the same
// way again, and a canceled or expired caller context ends the call.
func isRetryableLLMError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests:
			return true
		default:
			return apiErr.StatusCode >= http.StatusInternalServerError
		}
	}
	return true
}

// Response is the raw output and accounting from one Respond call.
type Response struct {
	Output   json.RawMessage
	Text     string
	Usage    Usage
	CacheHit bool
	// Truncated reports that the provider stopped at the request's output ceiling
	// (max_output_tokens), so Text is incomplete. It is never cached.
	Truncated bool
	// Incomplete reports that the provider did not complete the answer for
	// another reason (a content filter, a failure, or no reason given), so Text
	// is not usable either. It is never cached.
	Incomplete bool
}

// Usage tracks token billing.
type Usage struct {
	InputTokens  int `json:"InputTokens"`
	OutputTokens int `json:"OutputTokens"`
	TotalTokens  int `json:"TotalTokens"`
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
	if cfg.Retry.MaxAttempts <= 0 {
		cfg.Retry.MaxAttempts = DefaultLLMRetryAttempts
	}
	if cfg.Retry.InitialBackoff <= 0 {
		cfg.Retry.InitialBackoff = 250 * time.Millisecond
	}
	if cfg.Retry.MaxBackoff <= 0 {
		cfg.Retry.MaxBackoff = 8 * time.Second
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
	if base := responsesBaseURL(cfg.BaseURL); base != "" {
		opts = append(opts, option.WithBaseURL(base))
	}
	// Requests carry the platform's API key: never follow a redirect (Go's
	// policy would keep Authorization on a subdomain, another port or an
	// https→http downgrade), and fail a redirect answer, which the SDK would
	// otherwise decode as a success. The copy keeps the client's transport.
	baseClient := cfg.HTTPClient
	if baseClient == nil {
		baseClient = http.DefaultClient
	}
	opts = append(opts, option.WithHTTPClient(httpclient.WithoutRedirects(baseClient)), option.WithMiddleware(refuseRedirectAnswers))
	// We drive retries with retry-go so the SDK does not double-retry on top of us.
	opts = append(opts, option.WithMaxRetries(0))
	client := openai.NewClient(opts...)
	keyHash := sha256.Sum256([]byte(cfg.APIKey))
	return &openaiLLM{
		client:      &client,
		model:       cfg.Model,
		limiter:     cfg.Limiter,
		retry:       cfg.Retry,
		log:         cfg.Logger,
		tok:         tk,
		keyHashHex:  hex.EncodeToString(keyHash[:8]),
		ceilingWarn: rate.Sometimes{First: 1, Interval: time.Minute},
	}, nil
}

// refuseRedirectAnswers turns a 3xx answer, which the no-redirect client
// returns as is, into an error instead of letting the SDK decode it.
func refuseRedirectAnswers(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	resp, err := next(req)
	if err != nil || resp == nil || resp.StatusCode < http.StatusMultipleChoices || resp.StatusCode >= http.StatusBadRequest {
		return resp, err
	}
	_ = resp.Body.Close()
	return nil, errors.Errorf("llm endpoint answered redirect status %d; redirects are not followed", resp.StatusCode)
}

// responsesBaseURL returns the API root the SDK appends "responses" to. The
// service's other OpenAI-compatible settings name a gateway by its origin, with
// "/v1" optional, so an origin selects its /v1 root; a base URL with a path of
// its own (an Azure or a proxy prefix) is kept, without a trailing "/responses".
func responsesBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	path := strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/responses")
	if path = strings.TrimRight(path, "/"); path == "" {
		path = "/v1"
	}
	u.Path, u.RawPath = path+"/", ""
	return u.String()
}

// NewOpenAILLMFromSettings builds the plugin's Responses-API client from its
// settings, including the documented indexer.retry.* policy.
func NewOpenAILLMFromSettings(settings Settings, tok Tokenizer, logger logSDK.Logger) (LLM, error) {
	return NewOpenAILLM(LLMConfig{
		APIKey:    settings.LLM.APIKey,
		BaseURL:   settings.LLM.BaseURL,
		Model:     settings.LLM.IndexingModel,
		Retry:     settings.Indexer.Retry,
		Tokenizer: tok,
		Logger:    logger,
	})
}

type openaiLLM struct {
	client     *openai.Client
	model      string
	limiter    *rate.Limiter
	retry      RetrySettings
	log        logSDK.Logger
	tok        Tokenizer
	keyHashHex string
	// ceilingWarn rate-limits the warning that the provider generated more than
	// max_output_tokens.
	ceilingWarn rate.Sometimes
}

// Respond performs a single Responses-API call, retrying transient errors.
func (l *openaiLLM) Respond(ctx context.Context, req Request) (*Response, error) {
	model := req.Model
	if model == "" {
		model = l.model
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Wrapf(errRequestNotSent, "context ended: %v", err)
	}
	params, err := responseParams(model, req)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	resp, err := l.send(ctx, req, params)
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
	if usage.TotalTokens == 0 {
		// Some gateways report input and output but no total.
		usage.TotalTokens = int(satAdd(resp.Usage.InputTokens, resp.Usage.OutputTokens))
	}
	text := resp.OutputText()
	truncated, incomplete := answerState(resp, req.MaxOutTokens)
	out := &Response{Text: text, Usage: usage, Truncated: truncated, Incomplete: incomplete}
	if req.MaxOutTokens > 0 && usage.OutputTokens > req.MaxOutTokens && l.log != nil {
		l.ceilingWarn.Do(func() {
			// The reservation assumed the ceiling holds; the excess is settled as debt.
			l.log.Warn("pageindex.llm.output_ceiling_ignored",
				glog.String("model", model),
				glog.Int("max_output_tokens", req.MaxOutTokens),
				glog.Int("output_tokens", usage.OutputTokens),
			)
		})
	}
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
			glog.Bool("truncated", out.Truncated),
			glog.Bool("incomplete", out.Incomplete),
			glog.String("status", string(resp.Status)),
			glog.Duration("latency", time.Since(start)),
		)
	}
	return out, nil
}

// responseParams builds the Responses API request of req for model. A schema
// that does not decode fails before anything is sent (errRequestNotSent).
func responseParams(model string, req Request) (responses.ResponseNewParams, error) {
	params := responses.ResponseNewParams{
		Model: model,
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
	if len(req.Schema) == 0 {
		return params, nil
	}
	var schema map[string]any
	if err := json.Unmarshal(req.Schema, &schema); err != nil {
		return params, errors.Wrapf(errRequestNotSent, "decode schema: %v", err)
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
	return params, nil
}

// send sends params with the configured retry policy, admitting every retry
// through req.AdmitRetry first. Its error is the last request's own outcome,
// which decides what that request is charged.
func (l *openaiLLM) send(ctx context.Context, req Request, params responses.ResponseNewParams) (*responses.Response, error) {
	var resp *responses.Response
	attempt := 0
	var lastErr error
	err := retry.Do(func() error {
		attempt++
		if attempt > 1 && req.AdmitRetry != nil {
			if admitErr := req.AdmitRetry(attempt, lastErr); admitErr != nil {
				return retry.Unrecoverable(admitErr)
			}
		}
		if l.limiter != nil {
			if waitErr := l.limiter.Wait(ctx); waitErr != nil {
				// A wait that fails would fail again, so it is not retried.
				cause := errRequestNotSent
				if ctx.Err() == nil {
					cause = errDeadlineAhead
				}
				lastErr = errors.Wrapf(cause, "rate limiter: %v", waitErr)
				return retry.Unrecoverable(lastErr)
			}
		}
		out, callErr := l.client.Responses.New(ctx, params)
		if callErr != nil {
			lastErr = callErr
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
		retry.RetryIf(func(err error) bool { return isRetryableLLMError(ctx, err) }),
	)
	if err != nil && lastErr != nil && ctx.Err() != nil {
		// The context ended while waiting to retry: retry-go reports the context's
		// error, but the request still pending settlement is the last one sent,
		// whose own outcome decides its charge.
		return nil, lastErr
	}
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// answerState classifies resp, sent with the output ceiling maxOut. It is
// truncated when it stopped at the ceiling: the API marks it incomplete for
// max_output_tokens, or it used the whole ceiling, which also covers responses
// cut without a mark, a server that lowered the ceiling and echoes the lower
// value (vLLM clamps it to the context left), and an upstream that ignored it.
// Reasoning tokens count toward the ceiling, so a truncated answer's text may be
// empty. It is incomplete when it did not complete for another reason: any
// status other than completed (gateways that omit the status are taken as
// complete), such as a content filter, a failure, or incomplete with no reason.
// Neither kind of answer is usable.
func answerState(resp *responses.Response, maxOut int) (truncated, incomplete bool) {
	ceiling := int64(maxOut)
	if echoed := resp.MaxOutputTokens; echoed > 0 && (ceiling <= 0 || echoed < ceiling) {
		ceiling = echoed
	}
	switch {
	case resp.Status == responses.ResponseStatusIncomplete && resp.IncompleteDetails.Reason == "max_output_tokens":
		return true, false
	case ceiling > 0 && resp.Usage.OutputTokens >= ceiling:
		return true, false
	case resp.Status != "" && resp.Status != responses.ResponseStatusCompleted:
		return false, true
	}
	return false, false
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
func HashRequest(req Request) ([32]byte, error) {
	model := req.Model
	body, err := json.Marshal(req.Input)
	if err != nil {
		return [32]byte{}, errors.Wrap(err, "encode request input")
	}
	params := struct {
		MaxOut      int     `json:"max_out_tokens"`
		Temperature float32 `json:"temperature"`
		SchemaName  string  `json:"schema_name,omitempty"`
	}{req.MaxOutTokens, req.Temperature, req.SchemaName}
	pbytes, err := json.Marshal(params)
	if err != nil {
		return [32]byte{}, errors.Wrap(err, "encode request parameters")
	}
	return CacheKey(model, string(body), req.Schema, pbytes), nil
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
