// Package firecrawl integrates the Firecrawl v2 search API as a search engine.
package firecrawl

import (
	"bytes"
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
	defaultEndpoint    = "https://api.firecrawl.dev/v2/search"
	httpRequestTimeout = 30 * time.Second
	// logBodyLimit caps the number of response bytes logged for debugging.
	logBodyLimit = 4096
	// defaultLimit is the number of web results requested when none is configured.
	defaultLimit = 10
	// maxLimit mirrors the Firecrawl API upper bound for the result count.
	maxLimit             = 100
	firecrawlEngineName  = "firecrawl"
	firecrawlEngineType  = "firecrawl"
	firecrawlWebSourceID = "web"
)

// Option configures the SearchEngine instance.
type Option func(*SearchEngine)

// WithHTTPClient overrides the HTTP client used to communicate with Firecrawl.
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

// WithName overrides the default engine name used by the manager to identify this instance.
func WithName(name string) Option {
	return func(engine *SearchEngine) {
		trimmed := strings.TrimSpace(name)
		if trimmed != "" {
			engine.name = trimmed
		}
	}
}

// WithEndpoint overrides the Firecrawl endpoint, primarily for testing.
func WithEndpoint(endpoint string) Option {
	return func(engine *SearchEngine) {
		trimmed := strings.TrimSpace(endpoint)
		if trimmed != "" {
			engine.endpoint = trimmed
		}
	}
}

// WithLimit overrides the number of web results requested from Firecrawl.
// Values outside the [1, maxLimit] range are clamped at search time.
func WithLimit(limit int) Option {
	return func(engine *SearchEngine) {
		if limit > 0 {
			engine.limit = limit
		}
	}
}

// SearchEngine queries the Firecrawl v2 search endpoint and converts the response into search items.
type SearchEngine struct {
	apiKey   string
	client   *http.Client
	endpoint string
	limit    int
	logger   logSDK.Logger
	name     string
}

// NewSearchEngine constructs a Firecrawl-backed search engine using the provided API key.
// The apiKey parameter must be non-empty at search time.
// Options can customize the HTTP client, logging, endpoint, result limit, or engine name.
func NewSearchEngine(apiKey string, opts ...Option) *SearchEngine {
	engine := &SearchEngine{
		apiKey:   strings.TrimSpace(apiKey),
		client:   &http.Client{Timeout: httpRequestTimeout},
		endpoint: defaultEndpoint,
		limit:    defaultLimit,
		logger:   log.Logger.Named("firecrawl_search"),
		name:     firecrawlEngineName,
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
	return firecrawlEngineName
}

// Type returns the engine type identifier.
func (e *SearchEngine) Type() string {
	return firecrawlEngineType
}

// searchRequest models the subset of the Firecrawl v2 search request body we send.
// Only metadata (url/title/description) is needed, so scrapeOptions are intentionally omitted
// to avoid extra latency and credit usage from page scraping.
type searchRequest struct {
	Query   string   `json:"query"`
	Limit   int      `json:"limit"`
	Sources []string `json:"sources"`
}

// Search performs the Firecrawl request and returns the web results as SearchResultItem values.
func (e *SearchEngine) Search(ctx context.Context, query string) ([]search.SearchResultItem, error) {
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		return nil, errors.New("search query cannot be empty")
	}
	if strings.TrimSpace(e.apiKey) == "" {
		return nil, errors.New("firecrawl api key is not configured")
	}

	endpoint, err := url.Parse(e.endpoint)
	if err != nil {
		return nil, errors.Wrapf(err, "invalid firecrawl endpoint %q", e.endpoint)
	}

	limit := e.limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	payload := searchRequest{
		Query:   trimmedQuery,
		Limit:   limit,
		Sources: []string{firecrawlWebSourceID},
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.Wrap(err, "marshal firecrawl request")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, errors.Wrap(err, "create firecrawl request")
	}
	// The API key travels in the Authorization header and must never be logged.
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	logger := e.logger
	if ctx != nil {
		if ctxLogger := gmw.GetLogger(ctx); ctxLogger != nil {
			logger = ctxLogger.Named("firecrawl_search")
		}
	}

	if logger != nil {
		logger.Debug("outgoing http request",
			zap.String("method", req.Method),
			zap.String("url", endpoint.String()),
			zap.String("query", trimmedQuery),
			zap.Int("limit", limit),
		)
	}

	startAt := time.Now()
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "send firecrawl request")
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "read firecrawl response body")
	}

	truncatedBody, truncated := truncateForLog(respBody, logBodyLimit)
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
		return nil, errors.Errorf("firecrawl returned status %d: %s", resp.StatusCode, truncatedBody)
	}

	var parsed firecrawlResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, errors.Wrap(err, "unmarshal firecrawl response")
	}

	if !parsed.Success {
		msg := strings.TrimSpace(parsed.Error)
		if msg == "" {
			msg = strings.TrimSpace(parsed.Warning)
		}
		if msg == "" {
			msg = "unknown error"
		}
		return nil, errors.Errorf("firecrawl reported failure: %s", msg)
	}

	items := make([]search.SearchResultItem, 0, len(parsed.Data.Web))
	for _, result := range parsed.Data.Web {
		link := strings.TrimSpace(result.URL)
		if link == "" {
			continue
		}
		items = append(items, search.SearchResultItem{
			URL:     link,
			Name:    result.Title,
			Snippet: result.Description,
		})
	}

	return items, nil
}

// firecrawlResponse models the subset of fields required from the Firecrawl v2 search response.
type firecrawlResponse struct {
	Success bool          `json:"success"`
	Data    firecrawlData `json:"data"`
	Error   string        `json:"error"`
	Warning string        `json:"warning"`
}

type firecrawlData struct {
	Web []firecrawlWebResult `json:"web"`
}

type firecrawlWebResult struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// truncateForLog limits the payload logged for debugging and reports whether truncation occurred.
func truncateForLog(body []byte, limit int) (string, bool) {
	if len(body) <= limit {
		return string(body), false
	}
	return string(body[:limit]), true
}
