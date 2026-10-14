package bing

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

	appLog "github.com/Laisky/laisky-blog-graphql/library/log"
	"github.com/Laisky/laisky-blog-graphql/library/search/diagnostics"
)

// SearchEngineInterface defines the search engine methods.
type SearchEngineInterface interface {
	Search(query string) ([]BingAnswer, error)
}

// SearchEngine is an empty struct implementing SearchEngineInterface.
type SearchEngine struct {
	apikey string
	client *http.Client
	logger logSDK.Logger
}

const (
	httpRequestTimeout = 10 * time.Second
)

// NewSearchEngine is a constructor for SearchEngine.
func NewSearchEngine(apikey string) *SearchEngine {
	return &SearchEngine{
		apikey: strings.TrimSpace(apikey),
		client: &http.Client{Timeout: httpRequestTimeout},
		logger: appLog.Logger.Named("bing_search"),
	}
}

// BingAnswer represents an individual search result returned by Bing.
type BingAnswer struct {
	Type         string `json:"_type"`
	QueryContext struct {
		OriginalQuery string `json:"originalQuery"`
	} `json:"queryContext"`
	WebPages struct {
		WebSearchURL          string `json:"webSearchUrl"`
		TotalEstimatedMatches int    `json:"totalEstimatedMatches"`
		Value                 []struct {
			ID               string    `json:"id"`
			Name             string    `json:"name"`
			URL              string    `json:"url"`
			IsFamilyFriendly bool      `json:"isFamilyFriendly"`
			DisplayURL       string    `json:"displayUrl"`
			Snippet          string    `json:"snippet"`
			DateLastCrawled  time.Time `json:"dateLastCrawled"`
			SearchTags       []struct {
				Name    string `json:"name"`
				Content string `json:"content"`
			} `json:"searchTags,omitempty"`
			About []struct {
				Name string `json:"name"`
			} `json:"about,omitempty"`
		} `json:"value"`
	} `json:"webPages"`
	RelatedSearches struct {
		ID    string `json:"id"`
		Value []struct {
			Text         string `json:"text"`
			DisplayText  string `json:"displayText"`
			WebSearchURL string `json:"webSearchUrl"`
		} `json:"value"`
	} `json:"relatedSearches"`
	RankingResponse struct {
		Mainline struct {
			Items []struct {
				AnswerType  string `json:"answerType"`
				ResultIndex int    `json:"resultIndex"`
				Value       struct {
					ID string `json:"id"`
				} `json:"value"`
			} `json:"items"`
		} `json:"mainline"`
		Sidebar struct {
			Items []struct {
				AnswerType string `json:"answerType"`
				Value      struct {
					ID string `json:"id"`
				} `json:"value"`
			} `json:"items"`
		} `json:"sidebar"`
	} `json:"rankingResponse"`
}

// Search performs a web search using the Bing Search API and returns a slice of BingAnswer.
func (se *SearchEngine) Search(ctx context.Context, query string) (*BingAnswer, error) {
	const endpoint = "https://api.bing.microsoft.com/v7.0/search"
	if strings.TrimSpace(se.apikey) == "" {
		return nil, errors.Wrap(diagnostics.New("configuration", "internal"), "bing api key is not configured")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.Wrap(diagnostics.Wrap(err, "request"), "create search request")
	}

	// Add query parameters.
	params := req.URL.Query()
	params.Add("q", query)
	req.URL.RawQuery = params.Encode()

	// Set the subscription key header.
	req.Header.Add("Ocp-Apim-Subscription-Key", se.apikey)

	logger := diagnostics.Logger(ctx, se.logger)
	if logger == nil {
		logger = appLog.Logger.Named("bing_search")
	}
	logger = logger.With(zap.String("provider", "bing"))

	logger.Debug("outgoing http request",
		zap.String("method", req.Method),
	)

	startAt := time.Now()
	resp, err := se.client.Do(req)
	if err != nil {
		// net/http's outer URL error contains request inputs and provider credentials.
		// Preserve its typed underlying cause without retaining the sensitive request URL.
		var requestErr *url.Error
		if errors.As(err, &requestErr) {
			err = requestErr.Err
		}
		return nil, errors.Wrap(diagnostics.Transport(ctx, err), "failed to send request")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.WithStack(diagnostics.HTTP(resp.StatusCode))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(diagnostics.Wrap(diagnostics.Transport(ctx, err), "response"), "failed to read response body")
	}

	logger.Debug("incoming http response",
		zap.String("method", req.Method),
		zap.Int("status", resp.StatusCode),
		zap.Duration("cost", time.Since(startAt)),
	)

	result := new(BingAnswer)
	if err = json.Unmarshal(body, result); err != nil {
		return nil, errors.Wrap(diagnostics.Wrap(err, "decode"), "failed to unmarshal JSON response")
	}

	if len(result.WebPages.Value) == 0 {
		logger.Warn("bing search returned no results",
			zap.Int("status", resp.StatusCode),
		)
	}

	return result, nil
}
