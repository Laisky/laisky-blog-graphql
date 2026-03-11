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
// An optional name overrides the default engine identifier; when empty, the default is used.
func NewGoogleEngineAdapter(engine *google.SearchEngine, name ...string) (*GoogleEngineAdapter, error) {
	if engine == nil {
		return nil, errors.New("google search engine cannot be nil")
	}
	n := googleProgrammableEngineName
	if len(name) > 0 && name[0] != "" {
		n = name[0]
	}
	return &GoogleEngineAdapter{engine: engine, name: n}, nil
}

// Name returns the configured identifier for the Google engine.
func (a *GoogleEngineAdapter) Name() string {
	if a == nil || a.name == "" {
		return googleProgrammableEngineName
	}
	return a.name
}

// Type returns the engine type identifier.
func (a *GoogleEngineAdapter) Type() string {
	return "google"
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
