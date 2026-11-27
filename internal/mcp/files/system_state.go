package files

import (
	"bytes"
	"context"
	"database/sql"
	"strings"

	errors "github.com/Laisky/errors/v2"
)

// SystemStateMutator updates the active files owned by one system namespace.
// The map is loaded and persisted inside the caller's user-project transaction.
type SystemStateMutator func(map[string][]byte) error

// PublishPluginSummaryAndState atomically publishes plugin summary metadata and
// a system namespace state mutation under the authenticated project's mutation
// lock. The systemProject is intentionally separate because PageIndex namespaces
// its catalog by tenant while the user row retains its public project name.
func (s *Service) PublishPluginSummaryAndState(
	ctx context.Context,
	auth AuthContext,
	project, systemProject, path string,
	in PluginSummaryInput,
	mutate SystemStateMutator,
) (bool, error) {
	if err := s.validateAuth(auth); err != nil {
		return false, errors.WithStack(err)
	}
	if err := ValidateProject(project); err != nil {
		return false, errors.WithStack(err)
	}
	if err := ValidateProject(systemProject); err != nil {
		return false, errors.WithStack(err)
	}
	if err := ValidatePath(path); err != nil {
		return false, errors.WithStack(err)
	}
	if mutate == nil {
		return false, errors.New("system state mutator is nil")
	}

	owner := systemOwnerFromContext(ctx)
	if owner == "" {
		return false, errors.New("system_owner is required")
	}
	userCtx := contextWithSystemOwner(ctx, "")
	published := false
	err := s.lockProvider.WithProjectLock(userCtx, s.db, s.isPostgres, auth.APIKeyHash, project, s.settings.LockTimeout, func(tx *sql.Tx) error {
		curHash, err := s.activeContentHashTx(userCtx, tx, auth.APIKeyHash, project, path)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return errors.Wrap(err, "load plugin publication target")
		}
		if in.ExpectedContentHash != "" && curHash != "" && curHash != in.ExpectedContentHash {
			return nil
		}
		effHash := in.ExpectedContentHash
		if effHash == "" {
			effHash = curHash
		}

		state, err := s.loadSystemStateTx(ctx, tx, owner, systemProject)
		if err != nil {
			return err
		}
		if err := mutate(state); err != nil {
			return errors.Wrap(err, "mutate plugin system state")
		}
		if err := s.persistSystemStateTx(ctx, tx, owner, systemProject, state); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			rebindSQL(`UPDATE mcp_files SET
				file_summary = ?, summary_content_hash = ?, summary_word_count = ?,
				summary_source = ?, summary_model = ?, summary_prompt_version = ?,
				summary_generation_key = ?, summary_status = ?, summary_updated_at = ?,
				summary_error_code = ?, content_hash = ?
			WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = '' AND deleted = FALSE`, s.isPostgres),
			in.Summary,
			effHash,
			in.WordCount,
			string(in.Source),
			in.Model,
			in.PromptVersion,
			in.GenerationKey,
			string(in.Status),
			s.clock(),
			in.ErrorCode,
			effHash,
			auth.APIKeyHash,
			project,
			path,
		); err != nil {
			return errors.Wrap(err, "publish plugin summary and state")
		}
		published = true
		return nil
	})
	if err != nil {
		return false, errors.WithStack(err)
	}
	return published, nil
}

// DeleteWithSystemState deletes user files and the corresponding system state
// mutation in one transaction. It is used by transactional PageIndex handles.
func (s *Service) DeleteWithSystemState(
	ctx context.Context,
	auth AuthContext,
	project, systemProject, path string,
	recursive bool,
	mutate SystemStateMutator,
) (DeleteResult, error) {
	if err := s.validateAuth(auth); err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}
	if err := ValidateProject(project); err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}
	if err := ValidateProject(systemProject); err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}
	if err := ValidatePath(path); err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}
	if path == "" {
		return DeleteResult{}, errors.WithStack(NewError(ErrCodePermissionDenied, "root directory cannot be deleted", false))
	}
	if mutate == nil {
		return DeleteResult{}, errors.New("system state mutator is nil")
	}

	owner := systemOwnerFromContext(ctx)
	if owner == "" {
		return DeleteResult{}, errors.New("system_owner is required")
	}
	userCtx := contextWithSystemOwner(ctx, "")
	var deletedCount int
	err := s.lockProvider.WithProjectLock(userCtx, s.db, s.isPostgres, auth.APIKeyHash, project, s.settings.LockTimeout, func(tx *sql.Tx) error {
		now := s.clock()
		paths, err := s.resolveDeleteTargets(userCtx, tx, auth.APIKeyHash, project, path, recursive)
		if err != nil {
			return err
		}
		if len(paths) == 0 {
			return errors.WithStack(NewError(ErrCodeNotFound, "path not found", false))
		}
		snapshots, err := s.loadFilesForSnapshotTx(userCtx, tx, auth.APIKeyHash, project, paths)
		if err != nil {
			return err
		}
		for _, snap := range snapshots {
			if err := s.snapshotFileVersionTx(userCtx, tx, auth.APIKeyHash, project, snap.Path, snap.Content, snap.Size, snap.ID, now); err != nil {
				return err
			}
		}
		inClause, inArgs := buildInClause(paths, s.isPostgres, 6)
		query := rebindSQL(`UPDATE mcp_files SET deleted = TRUE, deleted_at = ?, updated_at = ?
			WHERE apikey_hash = ? AND project = ? AND deleted = FALSE AND system_owner = ? AND path IN (%s)`, s.isPostgres)
		args := make([]any, 0, 5+len(inArgs))
		args = append(args, now, now, auth.APIKeyHash, project, systemOwnerFromContext(userCtx))
		args = append(args, inArgs...)
		if _, err := tx.ExecContext(userCtx, strings.Replace(query, "%s", inClause, 1), args...); err != nil {
			return errors.Wrap(err, "soft delete files with system state")
		}
		for _, snap := range snapshots {
			if err := s.pruneVersionsTx(userCtx, tx, auth.APIKeyHash, project, snap.Path, now); err != nil {
				return err
			}
		}
		state, err := s.loadSystemStateTx(ctx, tx, owner, systemProject)
		if err != nil {
			return err
		}
		if err := mutate(state); err != nil {
			return errors.Wrap(err, "mutate delete system state")
		}
		if err := s.persistSystemStateTx(ctx, tx, owner, systemProject, state); err != nil {
			return err
		}
		deletedCount = len(paths)
		return nil
	})
	if err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}
	return DeleteResult{DeletedCount: deletedCount}, nil
}

// RenameWithSystemState renames user files and mutates the corresponding system
// state in one transaction. The existing rename validation and index-job logic
// are reused by the system-state variant.
func (s *Service) RenameWithSystemState(
	ctx context.Context,
	auth AuthContext,
	project, systemProject, fromPath, toPath string,
	overwrite bool,
	mutate SystemStateMutator,
) (RenameResult, error) {
	if mutate == nil {
		return RenameResult{}, errors.New("system state mutator is nil")
	}
	owner := systemOwnerFromContext(ctx)
	if owner == "" {
		return RenameResult{}, errors.New("system_owner is required")
	}
	if err := ValidateProject(systemProject); err != nil {
		return RenameResult{}, errors.WithStack(err)
	}
	userCtx := contextWithSystemOwner(ctx, "")
	req := renameRequest{project: project, fromPath: fromPath, targetProject: project, toPath: toPath, overwrite: overwrite}
	return s.rename(userCtx, auth, req, func(ctx context.Context, tx *sql.Tx, _ RenameStateChange) error {
		return s.mutateRenameSystemStateTx(ctx, tx, systemProject, owner, mutate)
	})
}

// RenameStateChange describes the exact file movements and displaced live
// paths observed by the transaction. System-state changes must use these paths.
type RenameStateChange struct {
	Mappings         []RenameMapping
	OverwrittenPaths []string
}

// SystemStateMoveMutator updates the source and target state in the rename
// transaction. A nil target means a same-namespace rename. Existing byte
// slices are read-only; replace map values rather than mutating them in place.
type SystemStateMoveMutator func(change RenameStateChange, source, target map[string][]byte) error

// SystemStateMovePlan bounds the read set for one plugin move. CatalogPaths
// are loaded from each namespace first. AffectedPaths selects the additional
// data needed for the exact FileIO changes, without reading unrelated bodies.
type SystemStateMovePlan struct {
	CatalogPaths  []string
	AffectedPaths func(change RenameStateChange, sourceCatalog, targetCatalog map[string][]byte) (source, target []string, err error)
	Mutate        SystemStateMoveMutator
}

// RenameMappedWithSystemState applies the file move and the corresponding
// state change under the participating project locks, in one transaction.
func (s *Service) RenameMappedWithSystemState(
	ctx context.Context,
	auth AuthContext,
	project, systemProject, fromPath string,
	targetProject, targetSystemProject, toPath string,
	overwrite bool,
	plan SystemStateMovePlan,
) (RenameResult, error) {
	if plan.Mutate == nil || plan.AffectedPaths == nil {
		return RenameResult{}, errors.New("system state move plan is incomplete")
	}
	owner := systemOwnerFromContext(ctx)
	if owner == "" {
		return RenameResult{}, errors.New("system_owner is required")
	}
	if err := ValidateProject(systemProject); err != nil {
		return RenameResult{}, errors.WithStack(err)
	}
	if err := ValidateProject(targetSystemProject); err != nil {
		return RenameResult{}, errors.WithStack(err)
	}
	if (project == targetProject) != (systemProject == targetSystemProject) {
		return RenameResult{}, errors.New("project and system namespace move scopes must agree")
	}
	userCtx := contextWithSystemOwner(ctx, "")
	req := renameRequest{project: project, fromPath: fromPath, targetProject: targetProject, toPath: toPath, overwrite: overwrite}
	return s.rename(userCtx, auth, req, func(ctx context.Context, tx *sql.Tx, change RenameStateChange) error {
		return s.mutateMoveSystemStateTx(ctx, tx, owner, systemProject, targetSystemProject, change, plan)
	})
}

func (s *Service) activeContentHashTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, path string) (string, error) {
	var hash string
	err := tx.QueryRowContext(ctx,
		rebindSQL(`SELECT content_hash FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = '' AND deleted = FALSE LIMIT 1`, s.isPostgres),
		apiKeyHash,
		project,
		path,
	).Scan(&hash)
	return hash, err
}

// loadSystemStateTx loads one namespace for ordinary plugin state mutations.
func (s *Service) loadSystemStateTx(ctx context.Context, tx *sql.Tx, owner, project string) (map[string][]byte, error) {
	state := make(map[string][]byte)
	err := s.readSystemStateTx(ctx, tx, owner, project, nil, state)
	return state, err
}

// readSelectedSystemStateTx reads only the named entries, in bounded batches
// so directory moves cannot hit the database's bind-parameter ceiling.
func (s *Service) readSelectedSystemStateTx(ctx context.Context, tx *sql.Tx, owner, project string, paths []string, state map[string][]byte) error {
	for len(paths) > 0 {
		n := min(len(paths), 200)
		if err := s.readSystemStateTx(ctx, tx, owner, project, paths[:n], state); err != nil {
			return err
		}
		paths = paths[n:]
	}
	return nil
}

// readSystemStateTx adds matching entries to state. A nil selection reads all rows.
func (s *Service) readSystemStateTx(ctx context.Context, tx *sql.Tx, owner, project string, paths []string, state map[string][]byte) error {
	query := rebindSQL(`SELECT path, content FROM mcp_files WHERE apikey_hash = ? AND project = ? AND system_owner = ? AND deleted = FALSE`, s.isPostgres)
	args := []any{"system:" + owner, project, owner}
	if len(paths) > 0 {
		clause, extra := buildInClause(paths, s.isPostgres, len(args)+1)
		query += " AND path IN (" + clause + ")"
		args = append(args, extra...)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return errors.Wrap(err, "load system state")
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var path string
		var content []byte
		if err := rows.Scan(&path, &content); err != nil {
			return errors.Wrap(err, "scan system state")
		}
		state[path] = content
	}
	if err := rows.Err(); err != nil {
		return errors.Wrap(err, "iterate system state")
	}
	return nil
}

func (s *Service) persistSystemStateTx(ctx context.Context, tx *sql.Tx, owner, project string, state map[string][]byte) error {
	if state == nil {
		return errors.New("system state is nil")
	}
	current, err := s.loadSystemStateTx(ctx, tx, owner, project)
	if err != nil {
		return err
	}
	return s.persistSystemStateDiffTx(ctx, tx, owner, project, current, state)
}

// persistSystemStateDiffTx persists changes against a snapshot taken in this transaction.
func (s *Service) persistSystemStateDiffTx(ctx context.Context, tx *sql.Tx, owner, project string, current, state map[string][]byte) error {
	now := s.clock()
	for path := range current {
		if _, ok := state[path]; ok {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			rebindSQL(`UPDATE mcp_files SET deleted = TRUE, deleted_at = ?, updated_at = ?
			WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = ? AND deleted = FALSE`, s.isPostgres),
			now,
			now,
			"system:"+owner,
			project,
			path,
			owner,
		); err != nil {
			return errors.Wrap(err, "delete system state file")
		}
	}
	for path, content := range state {
		if err := ValidatePath(path); err != nil {
			return errors.Wrap(err, "validate system state path")
		}
		if err := ValidateFileSize(int64(len(content)), s.settings.MaxFileBytes); err != nil {
			return errors.Wrap(err, "validate system state file")
		}
		if old, ok := current[path]; ok && bytes.Equal(old, content) {
			continue
		}
		var id uint64
		findErr := tx.QueryRowContext(ctx,
			rebindSQL(`SELECT id FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = ? AND deleted = FALSE LIMIT 1`, s.isPostgres),
			"system:"+owner,
			project,
			path,
			owner,
		).Scan(&id)
		switch {
		case errors.Is(findErr, sql.ErrNoRows):
			if _, err := tx.ExecContext(ctx,
				rebindSQL(`INSERT INTO mcp_files (apikey_hash, project, path, content, size, created_at, updated_at, deleted, deleted_at, system_owner, skip_rag_index, content_hash)
				VALUES (?, ?, ?, ?, ?, ?, ?, FALSE, NULL, ?, TRUE, ?)`, s.isPostgres),
				"system:"+owner,
				project,
				path,
				content,
				int64(len(content)),
				now,
				now,
				owner,
				HashFileContent(content),
			); err != nil {
				return errors.Wrap(err, "insert system state file")
			}
		case findErr != nil:
			return errors.Wrap(findErr, "find system state file")
		default:
			if _, err := tx.ExecContext(ctx,
				rebindSQL(`UPDATE mcp_files SET content = ?, size = ?, updated_at = ?, deleted = FALSE, deleted_at = NULL, skip_rag_index = TRUE, content_hash = ?
				WHERE id = ? AND system_owner = ?`, s.isPostgres),
				content,
				int64(len(content)),
				now,
				HashFileContent(content),
				id,
				owner,
			); err != nil {
				return errors.Wrap(err, "update system state file")
			}
		}
	}
	return nil
}
