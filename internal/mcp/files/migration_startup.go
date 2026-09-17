package files

import (
	"context"
	"database/sql"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// fileSchemaVersion is a durable checkpoint, not a process-local cache. Add a
// new numbered step when changing the schema; never repurpose a recorded step.
const fileSchemaVersion int64 = 1

const fileMigrationVersionQuery = `SELECT version FROM mcp_file_schema_migrations ORDER BY version DESC LIMIT 1`

// migrationExecutor lets all schema changes share one connection and transaction.
type migrationExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// RunMigrations automatically initializes/upgrades FileIO before NewService can
// return. Completed startups only read a small indexed migration ledger: they
// perform no DDL, file scans, backfill, trigger replacement or migration locking.
func RunMigrations(ctx context.Context, db *sql.DB, logger logSDK.Logger) error {
	if db == nil {
		return errors.New("sql db is required")
	}
	if logger == nil {
		logger = log.Logger.Named("mcp_files_migration")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	isPostgres, err := detectPostgresDialect(ctx, db)
	if err != nil {
		return errors.Wrap(err, "detect database dialect")
	}
	return runFileMigrations(ctx, db, isPostgres, func(tx *sql.Tx) error {
		return migrateFileSchema(ctx, tx, logger, isPostgres)
	})
}

// runFileMigrations checks before locking and rechecks after acquiring the
// database lock. Schema changes and their completion marker commit together;
// interrupted/failed attempts leave no success marker and are retried on startup.
func runFileMigrations(ctx context.Context, db *sql.DB, isPostgres bool, apply func(*sql.Tx) error) error {
	version, err := readFileMigrationVersion(ctx, db, isPostgres, true)
	if err != nil {
		return err
	}
	if version >= fileSchemaVersion {
		return validateFileSchemaVersion(version)
	}

	var opts *sql.TxOptions
	if isPostgres {
		opts = &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	}
	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return errors.Wrap(err, "begin file schema migration")
	}
	defer func() { _ = tx.Rollback() }()
	if isPostgres {
		// Separate subsystem key and schema-scoped lock. Transaction lifetime
		// avoids session-lock leakage through database/sql connection pooling.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(4606287, hashtext(current_schema()))`); err != nil {
			return errors.Wrap(err, "acquire file schema migration lock")
		}
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS mcp_file_schema_migrations (
		version BIGINT PRIMARY KEY CHECK (version > 0),
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return errors.Wrap(err, "initialize file schema ledger")
	}
	if !isPostgres {
		// Reserve SQLite's database writer BEFORE reading migration state, even
		// when CREATE TABLE IF NOT EXISTS was a no-op. No file rows are touched.
		if _, err := tx.ExecContext(ctx, `UPDATE mcp_file_schema_migrations SET version = version WHERE 1 = 0`); err != nil {
			return errors.Wrap(err, "reserve sqlite migration writer")
		}
	}
	version, err = readFileMigrationVersion(ctx, tx, isPostgres, false)
	if err != nil {
		return err
	}
	if version > fileSchemaVersion {
		return validateFileSchemaVersion(version)
	}
	if version < fileSchemaVersion {
		if err := apply(tx); err != nil {
			return errors.Wrap(err, "apply file schema migration")
		}
		if _, err := tx.ExecContext(ctx, rebindSQL(`INSERT INTO mcp_file_schema_migrations (version) VALUES (?)`, isPostgres), fileSchemaVersion); err != nil {
			return errors.Wrap(err, "record completed file schema migration")
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.Wrap(err, "commit file schema migration")
	}
	return nil
}

// fileMigrationCheckpointQuery prevents a ledger later in search_path from
// falsely marking a fresh PostgreSQL schema as migrated.
func fileMigrationCheckpointQuery(isPostgres bool) string {
	if isPostgres {
		return `SELECT version FROM mcp_file_schema_migrations
			WHERE tableoid IN (SELECT oid FROM pg_class
				WHERE relname = 'mcp_file_schema_migrations' AND relnamespace = current_schema()::regnamespace)
			ORDER BY version DESC LIMIT 1`
	}
	return fileMigrationVersionQuery
}

func readFileMigrationVersion(ctx context.Context, db migrationExecutor, isPostgres, allowMissing bool) (int64, error) {
	var version int64
	err := db.QueryRowContext(ctx, fileMigrationCheckpointQuery(isPostgres)).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if allowMissing && missingFileMigrationLedger(err) {
		return 0, nil
	}
	if err != nil {
		return 0, errors.Wrap(err, "read file schema migration checkpoint")
	}
	return version, nil
}

func missingFileMigrationLedger(err error) bool {
	if err == nil {
		return false
	}
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		return state.SQLState() == "42P01" // Undefined table in this exact ledger SELECT.
	}
	// Do not mistake permissions, connectivity, or arbitrary SQL errors for a
	// new database. SQLite exposes this diagnostic without importing a CGO driver.
	return strings.Contains(err.Error(), "no such table: mcp_file_schema_migrations")
}

func validateFileSchemaVersion(version int64) error {
	if version > fileSchemaVersion {
		return errors.Errorf("file schema version %d is newer than supported version %d", version, fileSchemaVersion)
	}
	return nil
}
