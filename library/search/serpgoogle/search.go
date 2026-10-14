package serpgoogle

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/library/log"
	"github.com/Laisky/laisky-blog-graphql/library/search"
	"github.com/Laisky/laisky-blog-graphql/library/search/diagnostics"
)

const (
	defaultEndpoint      = "https://serpapi.com/search.json"
	httpRequestTimeout   = 10 * time.Second
	serpGoogleEngineName = "serp_google"
)

// Option configures the SearchEngine instance.
type Option func(*SearchEngine)

// WithHTTPClient overrides the HTTP client used to communicate with SerpApi.
func WithHTTPClient(client *http.Client) Option {
	return func(engine *SearchEngine) {
		if client != nil {
			engine.client = client
		}
	}
}

// WithLogger overrides the default logger used for requests when no contextual logger is present.
func WithLogger(logger logSDK.Logger) Option {
	return func(engine *SearchEngine) {
		if logger != nil {
			engine.logger = logger
		}
	}
}

// WithDefaultParameters supplies key-value pairs that are added to every request.
func WithDefaultParameters(parameters map[string]string) Option {
	return func(engine *SearchEngine) {
		if len(parameters) == 0 {
			return
		}
		engine.defaultParams = make(map[string]string, len(parameters))
		for key, value := range parameters {
			engine.defaultParams[key] = value
		}
	}
}

// WithName overrides the default engine name used by the manager to identify this instance.
func WithName(name string) Option {
	return func(engine *SearchEngine) {
		trimmed := strings.TrimSpace(name)
		if trimmed != "" {
			engine.name = trimmed
		}
	}
}

// WithEndpoint overrides the SerpApi endpoint, primarily for testing.
func WithEndpoint(endpoint string) Option {
	return func(engine *SearchEngine) {
		trimmed := strings.TrimSpace(endpoint)
		if trimmed != "" {
			engine.endpoint = trimmed
		}
	}
}

// SearchEngine queries SerpApi's Google Search endpoint and converts the response into search items.
type SearchEngine struct {
	apiKey        string
	client        *http.Client
	endpoint      string
	defaultParams map[string]string
	logger        logSDK.Logger
	name          string
}

// NewSearchEngine constructs a SerpApi-backed search engine using the provided API key.
// The apiKey parameter must be non-empty at search time.
// Options can customize the HTTP client, logging, endpoint, or default parameters.
func NewSearchEngine(apiKey string, opts ...Option) *SearchEngine {
	engine := &SearchEngine{
		apiKey:        strings.TrimSpace(apiKey),
		client:        &http.Client{Timeout: httpRequestTimeout},
		endpoint:      defaultEndpoint,
		defaultParams: map[string]string{"engine": "google", "device": "desktop"},
		logger:        log.Logger.Named("serp_google"),
		name:          serpGoogleEngineName,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(engine)
		}
	}

	return engine
}

// Name returns the identifier used by the manager to distinguish the engine.
func (e *SearchEngine) Name() string {
	if e.name != "" {
		return e.name
	}
	return serpGoogleEngineName
}

// Type returns the engine type identifier.
func (e *SearchEngine) Type() string {
	return "serp_google"
}

// Search performs the SerpApi request and returns the organic results as SearchResultItem values.
func (e *SearchEngine) Search(ctx context.Context, query string) ([]search.SearchResultItem, error) {
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		return nil, errors.New("search query cannot be empty")
	}
	if strings.TrimSpace(e.apiKey) == "" {
		return nil, errors.Wrap(diagnostics.New("configuration", "internal"), "serp google api key is not configured")
	}

	endpoint, err := url.Parse(e.endpoint)
	if err != nil {
		return nil, diagnostics.Wrap(err, "configuration")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, errors.Wrap(diagnostics.Wrap(err, "request"), "create serp google request")
	}

	params := req.URL.Query()
	for key, value := range e.defaultParams {
		if _, exists := params[key]; !exists {
			params.Set(key, value)
		}
	}
	params.Set("q", trimmedQuery)
	params.Set("api_key", e.apiKey)
	req.URL.RawQuery = params.Encode()
	req.Header.Set("Accept", "application/json")

	logger := diagnostics.Logger(ctx, e.logger)

	if logger != nil {
		logger = logger.With(zap.String("provider", "serp_google"))
		logger.Debug("outgoing http request",
			zap.String("method", req.Method),
		)
	}

	startAt := time.Now()
	resp, err := e.client.Do(req)
	if err != nil {
		// net/http's outer URL error contains request inputs and provider credentials.
		// Preserve its typed underlying cause without retaining the sensitive request URL.
		var requestErr *url.Error
		if errors.As(err, &requestErr) {
			err = requestErr.Err
		}
		return nil, errors.Wrap(diagnostics.Transport(ctx, err), "send serp google request")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.WithStack(diagnostics.HTTP(resp.StatusCode))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(diagnostics.Wrap(diagnostics.Transport(ctx, err), "response"), "read serp google response body")
	}

	if logger != nil {
		logger.Debug("incoming http response",
			zap.Int("status", resp.StatusCode),
			zap.Int("response_bytes", len(body)),
			zap.Duration("cost", time.Since(startAt)),
		)
	}

	var payload serpResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.Wrap(diagnostics.Wrap(err, "decode"), "unmarshal serp google response")
	}

	if payload.Error != "" {
		return nil, errors.Wrap(diagnostics.New("provider", "unknown"), "serp google reported failure")
	}

	items := make([]search.SearchResultItem, 0, len(payload.OrganicResults))
	for _, result := range payload.OrganicResults {
		if strings.TrimSpace(result.Link) == "" {
			continue
		}
		items = append(items, search.SearchResultItem{
			URL:     result.Link,
			Name:    result.Title,
			Snippet: result.Snippet,
		})
	}

	return items, nil
}

// serpResponse models the subset of fields required from the SerpApi response.
type serpResponse struct {
	OrganicResults []serpOrganicResult `json:"organic_results"`
	Error          string              `json:"error"`
}

type serpOrganicResult struct {
	Link    string `json:"link"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
}
