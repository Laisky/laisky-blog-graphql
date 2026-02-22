package calllog

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestServiceRecordAndList(t *testing.T) {
	db, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(db.Close)

	ctx := context.Background()
	db.ExpectExec("CREATE TABLE IF NOT EXISTS mcp_call_logs").WillReturnResult(pgxmock.NewResult("CREATE TABLE", 0))
	db.ExpectExec("CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_tool_name").WillReturnResult(pgxmock.NewResult("CREATE INDEX", 0))
	db.ExpectExec("CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_api_key_hash").WillReturnResult(pgxmock.NewResult("CREATE INDEX", 0))
	db.ExpectExec("CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_key_prefix").WillReturnResult(pgxmock.NewResult("CREATE INDEX", 0))
	db.ExpectExec("CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_status").WillReturnResult(pgxmock.NewResult("CREATE INDEX", 0))
	db.ExpectExec("CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_occurred_at").WillReturnResult(pgxmock.NewResult("CREATE INDEX", 0))

	svc, err := NewService(db, nil, func() time.Time { return time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC) })
	require.NoError(t, err)

	db.ExpectExec(regexp.QuoteMeta("INSERT INTO mcp_call_logs")).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	require.NoError(t, svc.Record(ctx, RecordInput{
		ToolName:   "web_search",
		APIKey:     "abc123456",
		Status:     StatusSuccess,
		Cost:       42,
		Parameters: map[string]any{"query": "golang"},
	}))

	db.ExpectExec(regexp.QuoteMeta("INSERT INTO mcp_call_logs")).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
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

	db.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM mcp_call_logs")).WillReturnRows(
		pgxmock.NewRows([]string{"count"}).AddRow(int64(2)),
	)
	rows := pgxmock.NewRows([]string{"id", "tool_name", "api_key_hash", "key_prefix", "status", "cost", "cost_unit", "duration_millis", "parameters", "error_message", "occurred_at", "created_at", "updated_at"}).
		AddRow(uuid.New(), "web_fetch", "hash2", "def7890", StatusError, 10, "quota", int64(1500), []byte(`{"url":"https://example.com"}`), "failed", time.Now().UTC(), time.Now().UTC(), time.Now().UTC()).
		AddRow(uuid.New(), "web_search", "hash1", "abc1234", StatusSuccess, 42, "quota", int64(0), []byte(`{"query":"golang"}`), "", time.Now().UTC(), time.Now().UTC(), time.Now().UTC())
	db.ExpectQuery("SELECT id, tool_name, api_key_hash, key_prefix, status, cost, cost_unit").
		WithArgs(0, 10).
		WillReturnRows(rows)

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
	require.NoError(t, db.ExpectationsWereMet())
}

func TestServiceListFilters(t *testing.T) {
	db, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(db.Close)

	db.ExpectExec("CREATE TABLE IF NOT EXISTS mcp_call_logs").WillReturnResult(pgxmock.NewResult("CREATE TABLE", 0))
	db.ExpectExec("CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_tool_name").WillReturnResult(pgxmock.NewResult("CREATE INDEX", 0))
	db.ExpectExec("CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_api_key_hash").WillReturnResult(pgxmock.NewResult("CREATE INDEX", 0))
	db.ExpectExec("CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_key_prefix").WillReturnResult(pgxmock.NewResult("CREATE INDEX", 0))
	db.ExpectExec("CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_status").WillReturnResult(pgxmock.NewResult("CREATE INDEX", 0))
	db.ExpectExec("CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_occurred_at").WillReturnResult(pgxmock.NewResult("CREATE INDEX", 0))
	svc, err := NewService(db, nil, nil)
	require.NoError(t, err)

	alphaHash, _ := normalizeAPIKey("token-alpha")

	ctx := context.Background()
	db.ExpectQuery(`SELECT COUNT\(\*\) FROM mcp_call_logs WHERE api_key_hash = \$1`).
		WithArgs(alphaHash).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(1)))
	db.ExpectQuery("SELECT id, tool_name, api_key_hash, key_prefix, status, cost, cost_unit").
		WithArgs(alphaHash, 0, 20).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "tool_name", "api_key_hash", "key_prefix", "status", "cost", "cost_unit", "duration_millis", "parameters", "error_message", "occurred_at", "created_at", "updated_at"}).
				AddRow(uuid.New(), "web_search", alphaHash, "token-a", StatusSuccess, 0, "quota", int64(0), []byte(`{}`), "", time.Now().UTC(), time.Now().UTC(), time.Now().UTC()),
		)
	list, err := svc.List(ctx, ListOptions{APIKeyHash: alphaHash})
	require.NoError(t, err)
	require.Len(t, list.Entries, 1)
	require.Equal(t, "web_search", list.Entries[0].ToolName)
	require.NoError(t, db.ExpectationsWereMet())
}

func TestServiceRecordWithCancelledContext(t *testing.T) {
	db, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(db.Close)

	db.ExpectExec("CREATE TABLE IF NOT EXISTS mcp_call_logs").WillReturnResult(pgxmock.NewResult("CREATE TABLE", 0))
	db.ExpectExec("CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_tool_name").WillReturnResult(pgxmock.NewResult("CREATE INDEX", 0))
	db.ExpectExec("CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_api_key_hash").WillReturnResult(pgxmock.NewResult("CREATE INDEX", 0))
	db.ExpectExec("CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_key_prefix").WillReturnResult(pgxmock.NewResult("CREATE INDEX", 0))
	db.ExpectExec("CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_status").WillReturnResult(pgxmock.NewResult("CREATE INDEX", 0))
	db.ExpectExec("CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_occurred_at").WillReturnResult(pgxmock.NewResult("CREATE INDEX", 0))
	svc, err := NewService(db, nil, nil)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	db.ExpectExec(regexp.QuoteMeta("INSERT INTO mcp_call_logs")).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	err = svc.Record(ctx, RecordInput{
		ToolName: "web_search",
		APIKey:   "test-key",
	})
	require.NoError(t, err)
	require.NoError(t, db.ExpectationsWereMet())
}
