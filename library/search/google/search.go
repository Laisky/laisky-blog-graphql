package google

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"

	appLog "github.com/Laisky/laisky-blog-graphql/library/log"
)

// SearchEngine provides access to the Google Programmable Search API.
type SearchEngine struct {
	apiKey string
	cx     string
	client *http.Client
	logger logSDK.Logger
}

const (
	httpRequestTimeout = 10 * time.Second
	// logBodyLimit caps the number of response bytes logged for debugging.
	logBodyLimit       = 4096
	searchEndpoint     = "https://www.googleapis.com/customsearch/v1"
)

// NewSearchEngine instantiates a Programmable Search client with the given credentials.
func NewSearchEngine(apiKey, cx string) *SearchEngine {
	return &SearchEngine{
		apiKey: strings.TrimSpace(apiKey),
		cx:     strings.TrimSpace(cx),
		client: &http.Client{Timeout: httpRequestTimeout},
		logger: appLog.Logger.Named("google_search"),
	}
}

// CustomSearchResponse models the JSON payload returned by the Custom Search API.
type CustomSearchResponse struct {
	Kind              string             `json:"kind"`
	URL               *ResponseURL       `json:"url,omitempty"`
	Queries           *Queries           `json:"queries,omitempty"`
	SearchInformation *SearchInformation `json:"searchInformation,omitempty"`
	Items             []SearchResultItem `json:"items"`
}

// ResponseURL contains OpenSearch template metadata.
type ResponseURL struct {
	Type     string `json:"type"`
	Template string `json:"template"`
}

// Queries describes the request, previous, and next page metadata for a search.
type Queries struct {
	Request      []QueryInfo `json:"request"`
	NextPage     []QueryInfo `json:"nextPage,omitempty"`
	PreviousPage []QueryInfo `json:"previousPage,omitempty"`
}

// QueryInfo captures the parameters used for a particular search request.
type QueryInfo struct {
	Title          string `json:"title"`
	TotalResults   string `json:"totalResults"`
	SearchTerms    string `json:"searchTerms"`
	Count          int    `json:"count"`
	StartIndex     int    `json:"startIndex"`
	Language       string `json:"language,omitempty"`
	InputEncoding  string `json:"inputEncoding,omitempty"`
	OutputEncoding string `json:"outputEncoding,omitempty"`
	Safe           string `json:"safe,omitempty"`
	CX             string `json:"cx,omitempty"`
}

// SearchInformation provides aggregate stats about a query.
type SearchInformation struct {
	SearchTime            float64 `json:"searchTime"`
	FormattedSearchTime   string  `json:"formattedSearchTime"`
	TotalResults          string  `json:"totalResults"`
	FormattedTotalResults string  `json:"formattedTotalResults"`
}

// SearchResultItem represents a single search result item.
type SearchResultItem struct {
	Kind             string `json:"kind"`
	Title            string `json:"title"`
	HTMLTitle        string `json:"htmlTitle"`
	Link             string `json:"link"`
	DisplayLink      string `json:"displayLink"`
	Snippet          string `json:"snippet"`
	HTMLSnippet      string `json:"htmlSnippet"`
	CacheID          string `json:"cacheId,omitempty"`
	FormattedURL     string `json:"formattedUrl,omitempty"`
	HTMLFormattedURL string `json:"htmlFormattedUrl,omitempty"`
}

// Search executes a Google Programmable Search query and returns the parsed response.
func (se *SearchEngine) Search(ctx context.Context, query string) (*CustomSearchResponse, error) {
	if strings.TrimSpace(se.apiKey) == "" {
		return nil, errors.New("google api key is not configured")
	}
	if strings.TrimSpace(se.cx) == "" {
		return nil, errors.New("google search engine id (cx) is not configured")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchEndpoint, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create request to `%s`", searchEndpoint)
	}

	params := req.URL.Query()
	params.Set("key", se.apiKey)
	params.Set("cx", se.cx)
	params.Set("q", query)
	req.URL.RawQuery = params.Encode()

	logger := se.logger
	if logger == nil {
		logger = appLog.Logger.Named("google_search")
	}

	logger.Debug("outgoing http request",
		zap.String("method", req.Method),
		zap.String("url", req.URL.String()),
		zap.String("body", ""),
		zap.String("query", query),
	)

	startAt := time.Now()
	resp, err := se.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "failed to send request")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read response body")
	}

	truncatedBody, truncated := truncateForLog(body, logBodyLimit)
	logger.Debug("incoming http response",
		zap.String("method", req.Method),
		zap.String("url", req.URL.String()),
		zap.Int("status", resp.StatusCode),
		zap.String("body", truncatedBody),
		zap.Bool("body_truncated", truncated),
		zap.Duration("cost", time.Since(startAt)),
		zap.String("query", query),
	)

	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf("google search returned status %d: %s", resp.StatusCode, truncatedBody)
	}

	result := new(CustomSearchResponse)
	if err := json.Unmarshal(body, result); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal JSON response")
	}

	if len(result.Items) == 0 {
		logger.Warn("google search returned no results",
			zap.String("query", query),
			zap.Int("status", resp.StatusCode),
		)
	}

	return result, nil
}

func truncateForLog(body []byte, limit int) (string, bool) {
	if len(body) <= limit {
		return string(body), false
	}
	return string(body[:limit]), true
}
