package files_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestFileIOPostgresConcurrentStartup exercises real concurrent first migration
// attempts. Without FILEIO_TEST_POSTGRES_DSN it explicitly skips, not passes.
func TestFileIOPostgresConcurrentStartup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	f := &raceFixture{ctx: ctx, postgres: true}
	f.openPostgres(t) // Fresh random schema and two independent pools.
	for _, err := range parallelRaceCalls(t, ctx, 8, func(i int) error {
		return files.RunMigrations(ctx, f.db[i%2], nil)
	}) {
		require.NoError(t, err)
	}
	var count int
	require.NoError(t, f.db[0].QueryRowContext(ctx, "SELECT COUNT(*) FROM mcp_file_schema_migrations").Scan(&count))
	require.Equal(t, 1, count)

	// A repeat startup must not even touch the file table. Holding its strongest
	// lock is deterministic: a DDL/backfill/file scan would block until timeout.
	tx, err := f.db[0].BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, "LOCK TABLE mcp_files IN ACCESS EXCLUSIVE MODE")
	require.NoError(t, err)
	warmCtx, warmCancel := context.WithTimeout(ctx, 2*time.Second)
	defer warmCancel()
	require.NoError(t, files.RunMigrations(warmCtx, f.db[1], nil), "completed startup must not wait on mcp_files")
	require.NoError(t, tx.Rollback())
}
