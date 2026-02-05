package bing

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
	// logBodyLimit caps the number of response bytes logged for debugging.
	logBodyLimit       = 4096
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

// bingResponse is the full JSON response from the Bing Search API.
type bingResponse struct {
	WebPages struct {
		WebSearchURL          string       `json:"webSearchUrl"`
		TotalEstimatedMatches int          `json:"totalEstimatedMatches"`
		Value                 []BingAnswer `json:"value"`
	} `json:"webPages"`
}

// Search performs a web search using the Bing Search API and returns a slice of BingAnswer.
func (se *SearchEngine) Search(ctx context.Context, query string) (*BingAnswer, error) {
	const endpoint = "https://api.bing.microsoft.com/v7.0/search"
	if strings.TrimSpace(se.apikey) == "" {
		return nil, errors.New("bing api key is not configured")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create request to `%s`", endpoint)
	}

	// Add query parameters.
	params := req.URL.Query()
	params.Add("q", query)
	req.URL.RawQuery = params.Encode()

	// Set the subscription key header.
	req.Header.Add("Ocp-Apim-Subscription-Key", se.apikey)

	logger := se.logger
	if logger == nil {
		logger = appLog.Logger.Named("bing_search")
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
		return nil, errors.Errorf("bing search returned status %d: %s", resp.StatusCode, truncatedBody)
	}

	result := new(BingAnswer)
	if err = json.Unmarshal(body, result); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal JSON response")
	}

	if len(result.WebPages.Value) == 0 {
		logger.Warn("bing search returned no results",
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
