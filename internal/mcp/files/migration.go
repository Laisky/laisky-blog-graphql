package files

import (
	"context"
	"database/sql"
	"strings"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

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

	statements := []string{}
	if isPostgres {
		statements = []string{
			`CREATE UNIQUE INDEX IF NOT EXISTS uq_mcp_files_active ON mcp_files (apikey_hash, project, path) WHERE deleted = FALSE`,
			`CREATE INDEX IF NOT EXISTS idx_mcp_files_prefix ON mcp_files (apikey_hash, project, path text_pattern_ops) WHERE deleted = FALSE`,
			`CREATE INDEX IF NOT EXISTS idx_mcp_files_deleted_at ON mcp_files (deleted_at) WHERE deleted = TRUE`,
			`CREATE INDEX IF NOT EXISTS idx_mcp_file_chunks_prefix ON mcp_file_chunks (apikey_hash, project, file_path text_pattern_ops)`,
			`CREATE INDEX IF NOT EXISTS idx_mcp_file_index_jobs_pending ON mcp_file_index_jobs (status, available_at, id)`,
		}
	}

	for _, stmt := range statements {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return errors.Wrap(err, "create index")
		}
	}

	logger.Debug("mcp files migrations completed")
	return nil
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
