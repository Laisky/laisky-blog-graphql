package bing

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/pkg/errors"
)

// SearchEngineInterface defines the search engine methods.
type SearchEngineInterface interface {
	Search(query string) ([]BingAnswer, error)
}

// SearchEngine is an empty struct implementing SearchEngineInterface.
type SearchEngine struct {
	apikey string
}

// NewSearchEngine is a constructor for SearchEngine.
func NewSearchEngine(apikey string) *SearchEngine {
	return &SearchEngine{
		apikey: apikey,
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
func (se *SearchEngine) Search(query string) ([]BingAnswer, error) {
	const endpoint = "https://api.bing.microsoft.com/v7.0/search"
	// Replace with your valid subscription key.

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create request to `%s`", endpoint)
	}

	// Add query parameters.
	params := req.URL.Query()
	params.Add("q", query)
	req.URL.RawQuery = params.Encode()

	// Set the subscription key header.
	req.Header.Add("Ocp-Apim-Subscription-Key", se.apikey)

	client := new(http.Client)
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "failed to send request")
	}
	defer resp.Body.Close()

	// Read the HTTP response.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read response body")
	}

	// Unmarshal the JSON response.
	var br bingResponse
	if err = json.Unmarshal(body, &br); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal JSON response")
	}

	return br.WebPages.Value, nil
}
