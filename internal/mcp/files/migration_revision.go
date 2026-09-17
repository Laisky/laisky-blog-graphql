package files

import (
	"context"
	"database/sql"

	errors "github.com/Laisky/errors/v2"
)

// applyFileRevisionMigration installs one atomic, idempotent schema change.
// Database triggers maintain revisions for EVERY content/path/lifecycle writer,
// including older application binaries and internal system-namespace SQL. They
// do not make an old binary understand a new MCP precondition (see the manual).
func applyFileRevisionMigration(ctx context.Context, db *sql.DB, isPostgres bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Wrap(err, "begin file revision migration")
	}
	defer func() { _ = tx.Rollback() }()

	columns := []struct{ name, definition string }{
		{"incarnation_id", "TEXT NOT NULL DEFAULT ''"},
		{"revision", "BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0)"},
	}
	if isPostgres {
		// system_owner-checked: Atomic DDL/backfill covers every namespace.
		if _, err := tx.ExecContext(ctx, `LOCK TABLE mcp_files IN ACCESS EXCLUSIVE MODE`); err != nil {
			return errors.Wrap(err, "lock revision migration")
		}
		for _, column := range columns {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE mcp_files ADD COLUMN IF NOT EXISTS `+column.name+` `+column.definition); err != nil {
				return errors.Wrap(err, "add file revision column")
			}
		}
	} else {
		// Reserve SQLite's writer before inspecting schema; avoid a deferred
		// read-to-write upgrade race between simultaneous service constructors.
		if _, err := tx.ExecContext(ctx, `UPDATE mcp_files SET size = size WHERE 1 = 0 AND system_owner = ''`); err != nil {
			return errors.Wrap(err, "reserve sqlite revision migration")
		}
		present, err := revisionSQLiteColumns(ctx, tx)
		if err != nil {
			return err
		}
		for _, column := range columns {
			if present[column.name] {
				continue
			}
			if _, err := tx.ExecContext(ctx, `ALTER TABLE mcp_files ADD COLUMN `+column.name+` `+column.definition); err != nil {
				return errors.Wrap(err, "add sqlite file revision column")
			}
		}
	}
	for _, statement := range fileRevisionStatements(isPostgres) {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return errors.Wrap(err, "install file revision invariants")
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.Wrap(err, "commit file revision migration")
	}
	return nil
}

func revisionSQLiteColumns(ctx context.Context, tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(mcp_files)`)
	if err != nil {
		return nil, errors.Wrap(err, "inspect revision schema")
	}
	defer func() { _ = rows.Close() }()
	present := make(map[string]bool)
	for rows.Next() {
		var id, notNull, primaryKey int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&id, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, errors.Wrap(err, "scan revision schema")
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate revision schema")
	}
	return present, nil
}

func fileRevisionStatements(isPostgres bool) []string {
	if isPostgres {
		return []string{
			// system_owner-checked: Backfill all existing namespaces under the DDL lock; never rewrite an assigned identity.
			`UPDATE mcp_files SET incarnation_id = replace(gen_random_uuid()::text, '-', '') WHERE incarnation_id = ''`,
			`CREATE UNIQUE INDEX IF NOT EXISTS mcp_files_incarnation_idx ON mcp_files (incarnation_id)`,
			`CREATE OR REPLACE FUNCTION mcp_file_revision_guard() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				IF TG_OP = 'INSERT' THEN
					NEW.incarnation_id := replace(gen_random_uuid()::text, '-', '');
					NEW.revision := 1;
				ELSE
					IF OLD.revision >= 9223372036854775807 THEN
						RAISE EXCEPTION 'FILEIO_REVISION_EXHAUSTED' USING ERRCODE = '22003';
					END IF;
					NEW.incarnation_id := OLD.incarnation_id;
					NEW.revision := OLD.revision + 1;
				END IF;
				RETURN NEW;
			END;
			$$`,
			`DROP TRIGGER IF EXISTS mcp_file_revision_guard ON mcp_files`,
			`CREATE TRIGGER mcp_file_revision_guard BEFORE INSERT OR UPDATE OF content, path, deleted, apikey_hash, project, system_owner
				ON mcp_files FOR EACH ROW EXECUTE FUNCTION mcp_file_revision_guard()`,
		}
	}
	return []string{
		// system_owner-checked: Backfill all existing namespaces atomically; never rewrite an assigned identity.
		`UPDATE mcp_files SET incarnation_id = lower(hex(randomblob(16))) WHERE incarnation_id = ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS mcp_files_incarnation_idx ON mcp_files (incarnation_id)`,
		`CREATE TRIGGER IF NOT EXISTS mcp_file_revision_insert AFTER INSERT ON mcp_files
		BEGIN
			UPDATE mcp_files SET incarnation_id = lower(hex(randomblob(16))), revision = 1
			WHERE id = NEW.id AND system_owner = NEW.system_owner;
		END`,
		`CREATE TRIGGER IF NOT EXISTS mcp_file_revision_overflow BEFORE UPDATE OF content, path, deleted, apikey_hash, project, system_owner ON mcp_files
		WHEN OLD.revision >= 9223372036854775807
		BEGIN
			SELECT RAISE(ABORT, 'FILEIO_REVISION_EXHAUSTED');
		END`,
		`CREATE TRIGGER IF NOT EXISTS mcp_file_revision_update AFTER UPDATE OF content, path, deleted, apikey_hash, project, system_owner ON mcp_files
		BEGIN
			UPDATE mcp_files SET incarnation_id = OLD.incarnation_id, revision = OLD.revision + 1
			WHERE id = NEW.id AND system_owner = NEW.system_owner;
		END`,
	}
}
