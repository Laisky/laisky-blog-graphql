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
	gmw "github.com/Laisky/gin-middlewares/v7"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/library/log"
	"github.com/Laisky/laisky-blog-graphql/library/search"
)

const (
	defaultEndpoint      = "https://serpapi.com/search.json"
	httpRequestTimeout   = 10 * time.Second
	// logBodyLimit caps the number of response bytes logged for debugging.
	logBodyLimit         = 4096
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
}

// NewSearchEngine constructs a SerpApi-backed search engine using the provided API key.
// The apiKey parameter must be non-empty at search time.
// Options can customise the HTTP client, logging, endpoint, or default parameters.
func NewSearchEngine(apiKey string, opts ...Option) *SearchEngine {
	engine := &SearchEngine{
		apiKey:        strings.TrimSpace(apiKey),
		client:        &http.Client{Timeout: httpRequestTimeout},
		endpoint:      defaultEndpoint,
		defaultParams: map[string]string{"engine": "google", "device": "desktop"},
		logger:        log.Logger.Named("serp_google"),
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
	return serpGoogleEngineName
}

// Search performs the SerpApi request and returns the organic results as SearchResultItem values.
func (e *SearchEngine) Search(ctx context.Context, query string) ([]search.SearchResultItem, error) {
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		return nil, errors.New("search query cannot be empty")
	}
	if strings.TrimSpace(e.apiKey) == "" {
		return nil, errors.New("serp google api key is not configured")
	}

	endpoint, err := url.Parse(e.endpoint)
	if err != nil {
		return nil, errors.Wrapf(err, "invalid serp google endpoint %q", e.endpoint)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, errors.Wrap(err, "create serp google request")
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

	logger := e.logger
	if ctx != nil {
		if ctxLogger := gmw.GetLogger(ctx); ctxLogger != nil {
			logger = ctxLogger.Named("serp_google")
		}
	}

	if logger != nil {
		logger.Debug("outgoing http request",
			zap.String("method", req.Method),
			zap.String("url", req.URL.String()),
			zap.String("query", trimmedQuery),
		)
	}

	startAt := time.Now()
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "send serp google request")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "read serp google response body")
	}

	truncatedBody, truncated := truncateForLog(body, logBodyLimit)
	if logger != nil {
		logger.Debug("incoming http response",
			zap.Int("status", resp.StatusCode),
			zap.String("body", truncatedBody),
			zap.Bool("body_truncated", truncated),
			zap.Duration("cost", time.Since(startAt)),
			zap.String("query", trimmedQuery),
		)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf("serp google returned status %d: %s", resp.StatusCode, truncatedBody)
	}

	var payload serpResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.Wrap(err, "unmarshal serp google response")
	}

	if payload.Error != "" {
		return nil, errors.Errorf("serp google reported error: %s", payload.Error)
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

// truncateForLog limits the payload logged for debugging and reports whether truncation occurred.
func truncateForLog(body []byte, limit int) (string, bool) {
	if len(body) <= limit {
		return string(body), false
	}
	return string(body[:limit]), true
}
