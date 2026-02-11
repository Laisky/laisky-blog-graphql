package files

import (
	"context"
	"strings"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"gorm.io/gorm"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// RunMigrations ensures FileIO tables and indexes exist.
func RunMigrations(ctx context.Context, db *gorm.DB, logger logSDK.Logger) error {
	if db == nil {
		return errors.New("gorm db is required")
	}
	if logger == nil {
		logger = log.Logger.Named("mcp_files_migration")
	}

	if err := ensureVectorExtension(ctx, db, logger); err != nil {
		return errors.WithStack(err)
	}

	if err := db.WithContext(ctx).AutoMigrate(&File{}, &FileChunk{}, &FileChunkEmbedding{}, &FileChunkBM25{}, &FileIndexJob{}); err != nil {
		return errors.Wrap(err, "auto migrate mcp files tables")
	}

	statements := []string{}
	if isPostgresDialect(db) {
		statements = []string{
			`CREATE UNIQUE INDEX IF NOT EXISTS uq_mcp_files_active ON mcp_files (apikey_hash, project, path) WHERE deleted = FALSE`,
			`CREATE INDEX IF NOT EXISTS idx_mcp_files_prefix ON mcp_files (apikey_hash, project, path text_pattern_ops) WHERE deleted = FALSE`,
			`CREATE INDEX IF NOT EXISTS idx_mcp_files_deleted_at ON mcp_files (deleted_at) WHERE deleted = TRUE`,
			`CREATE INDEX IF NOT EXISTS idx_mcp_file_chunks_prefix ON mcp_file_chunks (apikey_hash, project, file_path text_pattern_ops)`,
			`CREATE INDEX IF NOT EXISTS idx_mcp_file_index_jobs_pending ON mcp_file_index_jobs (status, available_at, id)`,
		}
	}

	for _, stmt := range statements {
		if err := db.WithContext(ctx).Exec(stmt).Error; err != nil {
			return errors.Wrap(err, "create index")
		}
	}

	logger.Debug("mcp files migrations completed")
	return nil
}

// ensureVectorExtension creates the pgvector extension when available.
func ensureVectorExtension(ctx context.Context, db *gorm.DB, logger logSDK.Logger) error {
	if db == nil {
		return errors.New("gorm db is nil")
	}
	if !isPostgresDialect(db) {
		return nil
	}

	if err := db.WithContext(ctx).Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		if shouldFallbackToPgvector(err) {
			if logger != nil {
				logger.Debug("pgvector extension unavailable under name 'vector', retrying with legacy name")
			}
			if execErr := db.WithContext(ctx).Exec("CREATE EXTENSION IF NOT EXISTS pgvector").Error; execErr != nil {
				return errors.Wrap(execErr, "create pgvector extension")
			}
			return nil
		}
		return errors.Wrap(err, "create vector extension")
	}
	return nil
}

// isPostgresDialect reports whether the gorm dialector is Postgres.
func isPostgresDialect(db *gorm.DB) bool {
	if db == nil || db.Dialector == nil {
		return false
	}
	return strings.EqualFold(db.Dialector.Name(), "postgres")
}

// shouldFallbackToPgvector checks whether the error indicates a legacy extension name.
func shouldFallbackToPgvector(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "extension \"vector\"") && strings.Contains(msg, "not") && strings.Contains(msg, "available")
}
