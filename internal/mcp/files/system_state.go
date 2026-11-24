package files

import (
	"bytes"
	"context"
	"database/sql"

	errors "github.com/Laisky/errors/v2"
)

// SystemStateMutator updates the active files owned by one system namespace.
// The map is loaded and persisted inside the caller's user-project transaction.
type SystemStateMutator func(map[string][]byte) error

// PathMove is one user file moved within a single physical project.
type PathMove struct {
	From string
	To   string
}

// SystemStateHooks mirror user-file deletions and moves into plugin-owned system
// state. Each hook runs inside the user mutation transaction, once for every
// physical user project the mutation changed, against the state stored under
// SystemProject(thatProject). A mutation of an overlaid project can change files
// of DefaultProject, so a plugin must never assume the addressed project.
type SystemStateHooks struct {
	// SystemProject maps a physical user project to its system-namespace project.
	SystemProject func(userProject string) string
	// Deleted returns the state change for user files removed from one project,
	// including destinations replaced by an overwriting rename.
	Deleted func(paths []string) SystemStateMutator
	// Moved returns the state change for user files renamed within one project.
	Moved func(moves []PathMove) SystemStateMutator
}

// validate rejects incomplete hooks before any user file is changed.
func (h SystemStateHooks) validate() error {
	if h.SystemProject == nil || h.Deleted == nil || h.Moved == nil {
		return errors.New("system state hooks are incomplete")
	}
	return nil
}

// systemStateRun binds hooks to the system owner whose state they mutate.
type systemStateRun struct {
	owner string
	hooks SystemStateHooks
}

// deleted mirrors user files removed from one physical project.
func (r *systemStateRun) deleted(ctx context.Context, s *Service, tx *sql.Tx, project string, paths []string) error {
	if r == nil || len(paths) == 0 {
		return nil
	}
	return s.mutateSystemStateTx(ctx, tx, r.owner, r.hooks.SystemProject(project), r.hooks.Deleted(paths))
}

// moved mirrors user files renamed within one physical project.
func (r *systemStateRun) moved(ctx context.Context, s *Service, tx *sql.Tx, project string, moves []PathMove) error {
	if r == nil || len(moves) == 0 {
		return nil
	}
	return s.mutateSystemStateTx(ctx, tx, r.owner, r.hooks.SystemProject(project), r.hooks.Moved(moves))
}

// mutateSystemStateTx loads, mutates, and persists one system project's state.
func (s *Service) mutateSystemStateTx(ctx context.Context, tx *sql.Tx, owner, systemProject string, mutate SystemStateMutator) error {
	if err := ValidateProject(systemProject); err != nil {
		return errors.Wrap(err, "validate system project")
	}
	stateCtx := contextWithSystemOwner(ctx, owner)
	state, err := s.loadSystemStateTx(stateCtx, tx, owner, systemProject)
	if err != nil {
		return err
	}
	if err := mutate(state); err != nil {
		return errors.Wrap(err, "mutate plugin system state")
	}
	return s.persistSystemStateTx(stateCtx, tx, owner, systemProject, state)
}

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

// DeleteWithSystemState deletes user files and mirrors the deletion into the
// owner's system state in one transaction. It is used by transactional PageIndex
// handles. Files deleted from DefaultProject through an overlaid project update
// the DefaultProject state, never the addressed project's state.
func (s *Service) DeleteWithSystemState(
	ctx context.Context,
	auth AuthContext,
	project, path string,
	recursive bool,
	hooks SystemStateHooks,
) (DeleteResult, error) {
	if err := s.validateAuth(auth); err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}
	if err := ValidateProject(project); err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}
	if err := ValidatePath(path); err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}
	if path == "" {
		return DeleteResult{}, errors.WithStack(NewError(ErrCodePermissionDenied, "root directory cannot be deleted", false))
	}
	if err := hooks.validate(); err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}

	owner := systemOwnerFromContext(ctx)
	if owner == "" {
		return DeleteResult{}, errors.New("system_owner is required")
	}
	state := &systemStateRun{owner: owner, hooks: hooks}
	userCtx := contextWithSystemOwner(ctx, "")
	var deletedCount int
	err := s.runUserMutation(userCtx, auth.APIKeyHash, project, []string{path}, func(tx *sql.Tx, overlay bool) error {
		if overlay {
			conditions := filePreconditionsFromContext(userCtx, auth, project, path, FileOperationDelete)
			var err error
			deletedCount, err = s.deleteOverlayTx(userCtx, tx, auth, project, path, recursive, conditions, state)
			return err
		}
		paths, err := s.resolveDeleteTargets(userCtx, tx, auth.APIKeyHash, project, path, recursive)
		if err != nil {
			return err
		}
		if len(paths) == 0 {
			return errors.WithStack(NewError(ErrCodeNotFound, "path not found", false))
		}
		if err := s.softDeletePathsTx(userCtx, tx, auth, project, paths, s.clock(), false); err != nil {
			return err
		}
		if err := state.deleted(ctx, s, tx, project, paths); err != nil {
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

// RenameWithSystemState renames user files and mirrors the moves into the
// owner's system state in one transaction. The existing rename validation and
// index-job logic are reused by the system-state variant.
func (s *Service) RenameWithSystemState(
	ctx context.Context,
	auth AuthContext,
	project, fromPath, toPath string,
	overwrite bool,
	hooks SystemStateHooks,
) (RenameResult, error) {
	if err := hooks.validate(); err != nil {
		return RenameResult{}, errors.WithStack(err)
	}
	owner := systemOwnerFromContext(ctx)
	if owner == "" {
		return RenameResult{}, errors.New("system_owner is required")
	}
	userCtx := contextWithSystemOwner(ctx, "")
	return s.renameWithSystemState(userCtx, auth, project, fromPath, toPath, overwrite, &systemStateRun{owner: owner, hooks: hooks})
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

func (s *Service) loadSystemStateTx(ctx context.Context, tx *sql.Tx, owner, project string) (map[string][]byte, error) {
	rows, err := tx.QueryContext(ctx,
		rebindSQL(`SELECT path, content FROM mcp_files WHERE apikey_hash = ? AND project = ? AND system_owner = ? AND deleted = FALSE`, s.isPostgres),
		"system:"+owner,
		project,
		owner,
	)
	if err != nil {
		return nil, errors.Wrap(err, "load system state")
	}
	defer func() { _ = rows.Close() }()
	state := make(map[string][]byte)
	for rows.Next() {
		var path string
		var content []byte
		if err := rows.Scan(&path, &content); err != nil {
			return nil, errors.Wrap(err, "scan system state")
		}
		state[path] = append([]byte(nil), content...)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate system state")
	}
	return state, nil
}

func (s *Service) persistSystemStateTx(ctx context.Context, tx *sql.Tx, owner, project string, state map[string][]byte) error {
	if state == nil {
		return errors.New("system state is nil")
	}
	current, err := s.loadSystemStateTx(ctx, tx, owner, project)
	if err != nil {
		return err
	}
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
