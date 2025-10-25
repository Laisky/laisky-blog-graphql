package search

import (
	"context"
	"testing"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

type testEngine struct {
	name  string
	items []SearchResultItem
	err   error
	calls int
}

func (e *testEngine) Name() string {
	return e.name
}

func (e *testEngine) Search(context.Context, string) ([]SearchResultItem, error) {
	e.calls++
	if e.err != nil {
		return nil, e.err
	}
	return e.items, nil
}

func TestManagerSearchReturnsFirstSuccess(t *testing.T) {
	primary := &testEngine{
		name:  "primary",
		items: []SearchResultItem{{URL: "https://example.com", Name: "Example", Snippet: "Snippet"}},
	}
	fallback := &testEngine{name: "fallback", err: errors.New("should not be called")}

	manager, err := NewManager([][]Engine{{primary}, {fallback}})
	require.NoError(t, err)

	result, err := manager.Search(context.Background(), "golang")
	require.NoError(t, err)
	require.Equal(t, primary.items, result)
	require.Equal(t, 1, primary.calls)
	require.Equal(t, 0, fallback.calls)
}

func TestManagerSearchFallsBackAcrossTiers(t *testing.T) {
	primary := &testEngine{name: "primary", err: errors.New("outage")}
	secondary := &testEngine{
		name:  "secondary",
		items: []SearchResultItem{{URL: "https://fallback", Name: "Fallback"}},
	}

	manager, err := NewManager([][]Engine{{primary}, {secondary}})
	require.NoError(t, err)

	result, err := manager.Search(context.Background(), "rust")
	require.NoError(t, err)
	require.Equal(t, secondary.items, result)
	require.Equal(t, 1, primary.calls)
	require.Equal(t, 1, secondary.calls)
}

func TestManagerSearchExhaustedEngines(t *testing.T) {
	primary := &testEngine{name: "primary", err: errors.New("timeout")}
	secondary := &testEngine{name: "secondary", err: errors.New("quota")}

	manager, err := NewManager([][]Engine{{primary}, {secondary}})
	require.NoError(t, err)

	result, err := manager.Search(context.Background(), "python")
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "primary")
	require.Contains(t, err.Error(), "secondary")
	require.Equal(t, 1, primary.calls)
	require.Equal(t, 1, secondary.calls)
}

func TestManagerHonoursDistinctLimit(t *testing.T) {
	primary := &testEngine{name: "primary", err: errors.New("failure")}
	secondary := &testEngine{name: "secondary"}

	manager, err := NewManager([][]Engine{{primary}, {secondary}}, WithMaxDistinctEngines(1))
	require.NoError(t, err)

	_, err = manager.Search(context.Background(), "distributed systems")
	require.Error(t, err)
	require.Equal(t, 1, primary.calls)
	require.Equal(t, 0, secondary.calls)
}
