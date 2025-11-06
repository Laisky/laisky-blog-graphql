package calllog

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestServiceRecordAndList(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)

	ctx := context.Background()
	svc, err := NewService(db, nil, func() time.Time { return time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC) })
	require.NoError(t, err)

	require.NoError(t, svc.Record(ctx, RecordInput{
		ToolName:   "web_search",
		APIKey:     "abc123456",
		Status:     StatusSuccess,
		Cost:       42,
		Parameters: map[string]any{"query": "golang"},
	}))

	require.NoError(t, svc.Record(ctx, RecordInput{
		ToolName:     "web_fetch",
		APIKey:       "def789012",
		Status:       StatusError,
		Cost:         10,
		Parameters:   map[string]any{"url": "https://example.com"},
		ErrorMessage: "failed",
		Duration:     1500 * time.Millisecond,
		OccurredAt:   time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC),
	}))

	list, err := svc.List(ctx, ListOptions{Page: 1, PageSize: 10, SortField: sortFieldCreatedAt})
	require.NoError(t, err)
	require.Len(t, list.Entries, 2)
	require.Equal(t, int64(2), list.Total)

	first := list.Entries[0]
	require.Equal(t, "web_fetch", first.ToolName)
	require.Equal(t, StatusError, first.Status)
	require.Equal(t, int64(1500), first.DurationMillis)
	require.Equal(t, "def7890", first.KeyPrefix)
	require.Equal(t, "failed", first.ErrorMessage)
	require.Contains(t, first.Parameters, "url")

	second := list.Entries[1]
	require.Equal(t, "web_search", second.ToolName)
	require.Equal(t, "abc1234", second.KeyPrefix)
	require.Contains(t, second.Parameters, "query")
}

func TestServiceListFilters(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)

	svc, err := NewService(db, nil, nil)
	require.NoError(t, err)

	ctx := context.Background()
	early := time.Date(2024, 3, 10, 12, 0, 0, 0, time.UTC)
	late := time.Date(2024, 3, 12, 12, 0, 0, 0, time.UTC)

	require.NoError(t, svc.Record(ctx, RecordInput{ToolName: "web_search", APIKey: "token-alpha", OccurredAt: early}))
	require.NoError(t, svc.Record(ctx, RecordInput{ToolName: "web_fetch", APIKey: "token-beta", OccurredAt: late}))

	alphaHash, _ := normalizeAPIKey("token-alpha")

	list, err := svc.List(ctx, ListOptions{APIKeyHash: alphaHash})
	require.NoError(t, err)
	require.Len(t, list.Entries, 1)
	require.Equal(t, "web_search", list.Entries[0].ToolName)

	from := time.Date(2024, 3, 11, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 3, 12, 0, 0, 0, 0, time.UTC)
	list, err = svc.List(ctx, ListOptions{From: from, To: to})
	require.NoError(t, err)
	require.Empty(t, list.Entries)

	to = time.Date(2024, 3, 13, 0, 0, 0, 0, time.UTC)
	list, err = svc.List(ctx, ListOptions{From: from, To: to})
	require.NoError(t, err)
	require.Len(t, list.Entries, 1)
	require.Equal(t, "web_fetch", list.Entries[0].ToolName)
}
