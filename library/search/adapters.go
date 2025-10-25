package search

import (
	"context"

	"github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/library/search/google"
)

const googleProgrammableEngineName = "google_programmable_search"

// GoogleEngineAdapter exposes the existing Google Programmable Search client as a Manager engine.
type GoogleEngineAdapter struct {
	engine *google.SearchEngine
	name   string
}

// NewGoogleEngineAdapter wraps the provided google.SearchEngine so that it satisfies the Engine interface.
// It returns the initialised adapter or an error when the input engine is nil.
func NewGoogleEngineAdapter(engine *google.SearchEngine) (*GoogleEngineAdapter, error) {
	if engine == nil {
		return nil, errors.New("google search engine cannot be nil")
	}
	return &GoogleEngineAdapter{engine: engine, name: googleProgrammableEngineName}, nil
}

// Name returns the configured identifier for the Google engine.
func (a *GoogleEngineAdapter) Name() string {
	if a == nil || a.name == "" {
		return googleProgrammableEngineName
	}
	return a.name
}

// Search executes the underlying Google search and converts the response into SearchResultItem values.
// It returns the converted items or an error when the Google client fails.
func (a *GoogleEngineAdapter) Search(ctx context.Context, query string) ([]SearchResultItem, error) {
	if a == nil || a.engine == nil {
		return nil, errors.New("google engine adapter is not initialised")
	}

	resp, err := a.engine.Search(ctx, query)
	if err != nil {
		return nil, errors.Wrap(err, "google programmable search failed")
	}

	items := make([]SearchResultItem, 0, len(resp.Items))
	for _, item := range resp.Items {
		items = append(items, SearchResultItem{
			URL:     item.Link,
			Name:    item.Title,
			Snippet: item.Snippet,
		})
	}
	return items, nil
}
