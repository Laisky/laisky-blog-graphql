package files

import (
	"context"
	"database/sql"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	errors "github.com/Laisky/errors/v2"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// TestFileIOStartupCheckpointFastPath rejects any unexpected transaction, DDL,
// advisory lock, trigger replacement, or file-table access after completion.
func TestFileIOStartupCheckpointFastPath(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		for _, version := range []int64{fileSchemaVersion, fileSchemaVersion + 1} {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			mock.ExpectQuery(regexp.QuoteMeta(fileMigrationCheckpointQuery(postgres))).
				WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(version))
			called := false
			err = runFileMigrations(context.Background(), db, postgres, func(*sql.Tx) error {
				called = true
				return errors.New("migration callback must not run")
			})
			if version == fileSchemaVersion {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "newer than supported")
			}
			require.False(t, called)
			require.NoError(t, mock.ExpectationsWereMet())
			mock.ExpectClose()
			require.NoError(t, db.Close())
		}
	}
}

// TestFileIOStartupCheckpointErrors verifies that permission/connectivity errors
// cannot be mistaken for an absent database and trigger an attempted migration.
func TestFileIOStartupCheckpointErrors(t *testing.T) {
	for _, failure := range []error{
		&pgconn.PgError{Code: "42501", Message: "permission denied"},
		errors.New("connection reset"),
	} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		mock.ExpectQuery(regexp.QuoteMeta(fileMigrationCheckpointQuery(true))).WillReturnError(failure)
		err = runFileMigrations(context.Background(), db, true, func(*sql.Tx) error {
			t.Error("migration must not be attempted after a checkpoint read error")
			return nil
		})
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
		mock.ExpectClose()
		require.NoError(t, db.Close())
	}
}

// TestFileIOStartupRechecksAfterLock models a second process that initially sees
// no completed migration but acquires the lock after the first process commits.
func TestFileIOStartupRechecksAfterLock(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		query := regexp.QuoteMeta(fileMigrationCheckpointQuery(postgres))
		mock.ExpectQuery(query).WillReturnRows(sqlmock.NewRows([]string{"version"}))
		mock.ExpectBegin()
		if postgres {
			mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
		}
		mock.ExpectExec("CREATE TABLE IF NOT EXISTS mcp_file_schema_migrations").WillReturnResult(sqlmock.NewResult(0, 0))
		if !postgres {
			mock.ExpectExec("UPDATE mcp_file_schema_migrations SET version = version WHERE 1 = 0").WillReturnResult(sqlmock.NewResult(0, 0))
		}
		mock.ExpectQuery(query).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(fileSchemaVersion))
		mock.ExpectCommit()
		called := false
		err = runFileMigrations(context.Background(), db, postgres, func(*sql.Tx) error {
			called = true
			return nil
		})
		require.NoError(t, err)
		require.False(t, called)
		require.NoError(t, mock.ExpectationsWereMet())
		mock.ExpectClose()
		require.NoError(t, db.Close())
	}
}

// TestFileIOStartupReadOnlyRestart proves the real completed SQLite path needs
// no writer/DDL privileges and preserves both schema state and existing tokens.
func TestFileIOStartupReadOnlyRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.ToSlash(filepath.Join(t.TempDir(), "startup.db"))
	db, err := sql.Open("sqlite3", "file:"+path+"?_busy_timeout=5000&_journal_mode=WAL")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	// Use the real constructor: operators must not have to call a migration CLI.
	svc, err := NewService(db, versionsTestSettings(), nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	auth := AuthContext{APIKeyHash: "startup"}
	written, err := svc.Write(ctx, auth, "project", "/a", "retained bytes", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	var beforeSchema int
	require.NoError(t, db.QueryRowContext(ctx, "PRAGMA schema_version").Scan(&beforeSchema))
	readonly, err := sql.Open("sqlite3", "file:"+path+"?mode=ro&_busy_timeout=5000")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, readonly.Close()) })
	for range 3 {
		require.NoError(t, RunMigrations(ctx, readonly, nil))
	}
	var afterSchema, count int
	require.NoError(t, readonly.QueryRowContext(ctx, "PRAGMA schema_version").Scan(&afterSchema))
	require.Equal(t, beforeSchema, afterSchema)
	require.NoError(t, readonly.QueryRowContext(ctx, "SELECT COUNT(*) FROM mcp_file_schema_migrations").Scan(&count))
	require.Equal(t, 1, count)
	read, err := svc.Read(ctx, auth, "project", "/a", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "retained bytes", read.Content)
	require.Equal(t, written.Version, read.Version)
}

// TestFileIOStartupMigrationRollback rolls back real schema work and its marker.
// A later startup retries the whole atomic step, rather than trusting partial DDL.
func TestFileIOStartupMigrationRollback(t *testing.T) {
	for _, cancelAttempt := range []bool{false, true} {
		db := newTestDB(t)
		t.Cleanup(func() { require.NoError(t, db.Close()) })
		// Keep one connection so cancellation cannot discard the only connection
		// to the in-memory database before the independent postcondition query.
		keeper, err := db.Conn(context.Background())
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, keeper.Close()) })
		ctx, cancel := context.WithCancel(context.Background())
		err = runFileMigrations(ctx, db, false, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "CREATE TABLE fileio_rollback_probe (id INTEGER)"); err != nil {
				return err
			}
			if err := migrateFileSchema(ctx, tx, nil, false); err != nil {
				return err
			}
			if cancelAttempt {
				cancel()
				return ctx.Err()
			}
			return errors.New("injected migration failure")
		})
		cancel()
		require.Error(t, err)
		var count int
		require.NoError(t, db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND (name = 'fileio_rollback_probe' OR name LIKE 'mcp_file%')").Scan(&count))
		require.Zero(t, count, "failed migration must not leave DDL or a completion marker")
		require.NoError(t, RunMigrations(context.Background(), db, nil))
		version, err := readFileMigrationVersion(context.Background(), db, false, false)
		require.NoError(t, err)
		require.Equal(t, fileSchemaVersion, version)
	}
}

// TestFileIOStartupConcurrentSQLite uses independent connection pools starting
// against a fresh real WAL database, not a single connection or process mutex.
func TestFileIOStartupConcurrentSQLite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "startup.db")) + "?_busy_timeout=10000&_journal_mode=WAL"
	const clients = 8
	var pools [clients]*sql.DB
	for i := range pools {
		db, err := sql.Open("sqlite3", dsn)
		require.NoError(t, err)
		require.NoError(t, db.PingContext(ctx))
		pools[i] = db
		t.Cleanup(func() { require.NoError(t, db.Close()) })
	}
	start := make(chan struct{})
	done := make(chan error, clients)
	for _, db := range pools {
		go func() {
			<-start
			done <- RunMigrations(ctx, db, nil)
		}()
	}
	close(start)
	for range clients {
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal("concurrent constructors did not finish")
		}
	}
	var count int
	require.NoError(t, pools[0].QueryRowContext(ctx, "SELECT COUNT(*) FROM mcp_file_schema_migrations").Scan(&count))
	require.Equal(t, 1, count)
}
