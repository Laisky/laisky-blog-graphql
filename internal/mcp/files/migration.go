package files

import (
	"context"
	"database/sql"
	"strings"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// systemOwnerTables enumerates every mcp_files-family table that carries a
// system_owner column under proposal §2.6.3.
var systemOwnerTables = []string{
	"mcp_files",
	"mcp_file_chunks",
	"mcp_file_chunk_embeddings",
	"mcp_file_chunk_bm25",
	"mcp_file_index_jobs",
	"mcp_file_versions",
}

// RunMigrations ensures FileIO tables and indexes exist.
func RunMigrations(ctx context.Context, db *sql.DB, logger logSDK.Logger) error {
	if db == nil {
		return errors.New("sql db is required")
	}
	if logger == nil {
		logger = log.Logger.Named("mcp_files_migration")
	}

	isPostgres, err := detectPostgresDialect(ctx, db)
	if err != nil {
		return errors.Wrap(err, "detect database dialect")
	}

	if err := ensureVectorExtension(ctx, db, logger, isPostgres); err != nil {
		return errors.WithStack(err)
	}

	for _, stmt := range migrationTableStatements(isPostgres) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return errors.Wrap(err, "create mcp files tables")
		}
	}

	if err := applySystemOwnerColumns(ctx, db, isPostgres); err != nil {
		return errors.WithStack(err)
	}

	if err := applySkipRAGIndexColumn(ctx, db, isPostgres); err != nil {
		return errors.WithStack(err)
	}

	statements := []string{}
	if isPostgres {
		statements = []string{
			`CREATE UNIQUE INDEX IF NOT EXISTS uq_mcp_files_active ON mcp_files (apikey_hash, project, path) WHERE deleted = FALSE`,
			`CREATE INDEX IF NOT EXISTS idx_mcp_files_prefix ON mcp_files (apikey_hash, project, path text_pattern_ops) WHERE deleted = FALSE`,
			`CREATE INDEX IF NOT EXISTS idx_mcp_files_deleted_at ON mcp_files (deleted_at) WHERE deleted = TRUE`,
			`CREATE INDEX IF NOT EXISTS idx_mcp_file_chunks_prefix ON mcp_file_chunks (apikey_hash, project, file_path text_pattern_ops)`,
			`CREATE INDEX IF NOT EXISTS idx_mcp_file_index_jobs_pending ON mcp_file_index_jobs (status, available_at, id)`,
			`CREATE INDEX IF NOT EXISTS idx_mcp_file_versions_path ON mcp_file_versions (apikey_hash, project, path, created_at DESC)`,
		}
	}

	for _, stmt := range statements {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return errors.Wrap(err, "create index")
		}
	}

	for _, stmt := range systemOwnerIndexStatements() {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return errors.Wrap(err, "create system_owner index")
		}
	}

	logger.Debug("mcp files migrations completed")
	return nil
}

// applySystemOwnerColumns adds the system_owner column on every mcp_files-family
// table. The migration is idempotent across Postgres and SQLite per §3.7.
func applySystemOwnerColumns(ctx context.Context, db *sql.DB, isPostgres bool) error {
	for _, table := range systemOwnerTables {
		if isPostgres {
			stmt := `ALTER TABLE ` + table + ` ADD COLUMN IF NOT EXISTS system_owner TEXT NOT NULL DEFAULT ''`
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				return errors.Wrapf(err, "add system_owner column on %s", table)
			}
			continue
		}
		if err := applyAddColumnIfMissing(ctx, db, table, "system_owner",
			`ALTER TABLE `+table+` ADD COLUMN system_owner TEXT NOT NULL DEFAULT ''`); err != nil {
			return errors.Wrapf(err, "add system_owner column on %s", table)
		}
	}
	return nil
}

// applySkipRAGIndexColumn adds mcp_files.skip_rag_index to honor WriteOpts.SkipRAGIndex
// per §2.6.1. The flag is denormalized only on mcp_files; index workers consult it
// before enqueuing jobs.
func applySkipRAGIndexColumn(ctx context.Context, db *sql.DB, isPostgres bool) error {
	if isPostgres {
		stmt := `ALTER TABLE mcp_files ADD COLUMN IF NOT EXISTS skip_rag_index BOOLEAN NOT NULL DEFAULT FALSE`
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return errors.Wrap(err, "add skip_rag_index column on mcp_files")
		}
		return nil
	}
	return applyAddColumnIfMissing(ctx, db, "mcp_files", "skip_rag_index",
		`ALTER TABLE mcp_files ADD COLUMN skip_rag_index BOOLEAN NOT NULL DEFAULT 0`)
}

// applyAddColumnIfMissing emulates ADD COLUMN IF NOT EXISTS for SQLite, which lacked
// native support before 3.35. We probe PRAGMA table_info first and only run the ALTER
// when the column is absent, so the migration is safe to re-run.
func applyAddColumnIfMissing(ctx context.Context, db *sql.DB, table, column, ddl string) error {
	exists, err := sqliteColumnExists(ctx, db, table, column)
	if err != nil {
		return errors.Wrapf(err, "probe %s.%s", table, column)
	}
	if exists {
		return nil
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return errors.Wrapf(err, "add column %s.%s", table, column)
	}
	return nil
}

// sqliteColumnExists returns true when PRAGMA table_info reports the column.
func sqliteColumnExists(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, errors.Wrap(err, "pragma table_info")
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, errors.Wrap(err, "scan pragma row")
		}
		if name == column {
			return true, nil
		}
	}
	return false, errors.WithStack(rows.Err())
}

// systemOwnerIndexStatements returns the supporting indexes for system_owner predicates.
// Indexes are intentionally identical across SQLite and Postgres: SQLite ignores the
// trailing column ordering hints we don't include here, and both engines benefit from
// (system_owner, apikey_hash, project, path).
func systemOwnerIndexStatements() []string {
	return []string{
		`CREATE INDEX IF NOT EXISTS mcp_files_system_owner_idx ON mcp_files (system_owner, apikey_hash, project, path)`,
		`CREATE INDEX IF NOT EXISTS mcp_file_chunks_system_owner_idx ON mcp_file_chunks (system_owner, apikey_hash, project, file_path)`,
		`CREATE INDEX IF NOT EXISTS mcp_file_index_jobs_system_owner_idx ON mcp_file_index_jobs (system_owner, status, available_at)`,
		`CREATE INDEX IF NOT EXISTS mcp_file_versions_system_owner_idx ON mcp_file_versions (system_owner, apikey_hash, project, path, created_at DESC)`,
	}
}

// migrationTableStatements returns CREATE TABLE statements for supported databases.
func migrationTableStatements(isPostgres bool) []string {
	if isPostgres {
		return []string{
			`CREATE TABLE IF NOT EXISTS mcp_files (
				id BIGSERIAL PRIMARY KEY,
				apikey_hash VARCHAR(64) NOT NULL,
				project VARCHAR(128) NOT NULL,
				path VARCHAR(1024) NOT NULL,
				content BYTEA NOT NULL,
				size BIGINT NOT NULL,
				created_at TIMESTAMPTZ NOT NULL,
				updated_at TIMESTAMPTZ NOT NULL,
				deleted BOOLEAN NOT NULL DEFAULT FALSE,
				deleted_at TIMESTAMPTZ
			)`,
			`CREATE TABLE IF NOT EXISTS mcp_file_chunks (
				id BIGSERIAL PRIMARY KEY,
				apikey_hash VARCHAR(64) NOT NULL,
				project VARCHAR(128) NOT NULL,
				file_path VARCHAR(1024) NOT NULL,
				chunk_index INTEGER NOT NULL,
				start_byte BIGINT NOT NULL,
				end_byte BIGINT NOT NULL,
				chunk_content TEXT NOT NULL,
				content_hash VARCHAR(64) NOT NULL,
				created_at TIMESTAMPTZ NOT NULL,
				updated_at TIMESTAMPTZ NOT NULL,
				last_served_at TIMESTAMPTZ
			)`,
			`CREATE TABLE IF NOT EXISTS mcp_file_chunk_embeddings (
				chunk_id BIGINT PRIMARY KEY,
				embedding vector(1536) NOT NULL,
				model VARCHAR(128) NOT NULL,
				created_at TIMESTAMPTZ NOT NULL,
				updated_at TIMESTAMPTZ NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS mcp_file_chunk_bm25 (
				chunk_id BIGINT PRIMARY KEY,
				tokens JSONB NOT NULL,
				token_count INTEGER NOT NULL,
				tokenizer VARCHAR(64) NOT NULL,
				created_at TIMESTAMPTZ NOT NULL,
				updated_at TIMESTAMPTZ NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS mcp_file_index_jobs (
				id BIGSERIAL PRIMARY KEY,
				apikey_hash VARCHAR(64) NOT NULL,
				project VARCHAR(128) NOT NULL,
				file_path VARCHAR(1024) NOT NULL,
				operation VARCHAR(16) NOT NULL,
				file_updated_at TIMESTAMPTZ,
				status VARCHAR(16) NOT NULL,
				retry_count INTEGER NOT NULL,
				available_at TIMESTAMPTZ NOT NULL,
				created_at TIMESTAMPTZ NOT NULL,
				updated_at TIMESTAMPTZ NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS mcp_file_versions (
				id BIGSERIAL PRIMARY KEY,
				apikey_hash VARCHAR(64) NOT NULL,
				project VARCHAR(128) NOT NULL,
				path VARCHAR(1024) NOT NULL,
				content BYTEA NOT NULL,
				size BIGINT NOT NULL,
				created_at TIMESTAMPTZ NOT NULL,
				source_file_id BIGINT
			)`,
		}
	}

	return []string{
		`CREATE TABLE IF NOT EXISTS mcp_files (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			apikey_hash TEXT NOT NULL,
			project TEXT NOT NULL,
			path TEXT NOT NULL,
			content BLOB NOT NULL,
			size INTEGER NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			deleted BOOLEAN NOT NULL DEFAULT FALSE,
			deleted_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_file_chunks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			apikey_hash TEXT NOT NULL,
			project TEXT NOT NULL,
			file_path TEXT NOT NULL,
			chunk_index INTEGER NOT NULL,
			start_byte INTEGER NOT NULL,
			end_byte INTEGER NOT NULL,
			chunk_content TEXT NOT NULL,
			content_hash TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			last_served_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_file_chunk_embeddings (
			chunk_id INTEGER PRIMARY KEY,
			embedding TEXT NOT NULL,
			model TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_file_chunk_bm25 (
			chunk_id INTEGER PRIMARY KEY,
			tokens TEXT NOT NULL,
			token_count INTEGER NOT NULL,
			tokenizer TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_file_index_jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			apikey_hash TEXT NOT NULL,
			project TEXT NOT NULL,
			file_path TEXT NOT NULL,
			operation TEXT NOT NULL,
			file_updated_at DATETIME,
			status TEXT NOT NULL,
			retry_count INTEGER NOT NULL,
			available_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_file_versions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			apikey_hash TEXT NOT NULL,
			project TEXT NOT NULL,
			path TEXT NOT NULL,
			content BLOB NOT NULL,
			size INTEGER NOT NULL,
			created_at DATETIME NOT NULL,
			source_file_id INTEGER
		)`,
	}
}

// ensureVectorExtension creates the pgvector extension when available.
func ensureVectorExtension(ctx context.Context, db *sql.DB, logger logSDK.Logger, isPostgres bool) error {
	if db == nil {
		return errors.New("sql db is nil")
	}
	if !isPostgres {
		return nil
	}

	if _, err := db.ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		if shouldFallbackToPgvector(err) {
			if logger != nil {
				logger.Debug("pgvector extension unavailable under name 'vector', retrying with legacy name")
			}
			if _, execErr := db.ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS pgvector"); execErr != nil {
				return errors.Wrap(execErr, "create pgvector extension")
			}
			return nil
		}
		return errors.Wrap(err, "create vector extension")
	}
	return nil
}

// shouldFallbackToPgvector checks whether the error indicates a legacy extension name.
func shouldFallbackToPgvector(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "extension \"vector\"") && strings.Contains(msg, "not") && strings.Contains(msg, "available")
}
