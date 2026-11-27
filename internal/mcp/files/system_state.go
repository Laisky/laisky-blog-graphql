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

// DeleteWithSystemState deletes user files and applies mutate to one system
// namespace in the same transaction. The namespace is bound to the addressed
// project, so a delete that resolves to a DefaultProject file is refused here;
// DeleteMappedWithSystemState maps every physical project to its own namespace.
func (s *Service) DeleteWithSystemState(
	ctx context.Context,
	auth AuthContext,
	project, systemProject, path string,
	recursive bool,
	mutate SystemStateMutator,
) (DeleteResult, error) {
	if mutate == nil {
		return DeleteResult{}, errors.New("system state mutator is nil")
	}
	if err := ValidateProject(systemProject); err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}
	return s.deleteWithSystemState(ctx, auth, project, path, recursive, func(ctx context.Context, tx *sql.Tx, owner string, change RenameStateChange) error {
		if err := requireAddressedProject(project, change); err != nil {
			return err
		}
		return s.mutateRenameSystemStateTx(ctx, tx, systemProject, owner, mutate)
	})
}

// DeleteMappedWithSystemState deletes user files and applies plan to the system
// namespace of every physical project the delete changed, in one transaction.
// A path that resolves to a DefaultProject file changes DefaultProject's state.
func (s *Service) DeleteMappedWithSystemState(ctx context.Context, auth AuthContext, project, path string, recursive bool, plan SystemStateMovePlan) (DeleteResult, error) {
	if err := plan.validate(); err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}
	return s.deleteWithSystemState(ctx, auth, project, path, recursive, func(ctx context.Context, tx *sql.Tx, owner string, change RenameStateChange) error {
		return s.mutateMoveSystemStateTx(ctx, tx, owner, change, plan)
	})
}

// deleteWithSystemState runs a user-file delete for the system owner of ctx and
// mirrors each physical change through apply.
func (s *Service) deleteWithSystemState(ctx context.Context, auth AuthContext, project, path string, recursive bool,
	apply func(ctx context.Context, tx *sql.Tx, owner string, change RenameStateChange) error,
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
	owner := systemOwnerFromContext(ctx)
	if owner == "" {
		return DeleteResult{}, errors.New("system_owner is required")
	}
	deleted, err := s.deleteFiles(contextWithSystemOwner(ctx, ""), auth, project, path, recursive, func(ctx context.Context, tx *sql.Tx, change RenameStateChange) error {
		return apply(ctx, tx, owner, change)
	})
	if err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}
	return DeleteResult{DeletedCount: deleted}, nil
}

// RenameWithSystemState renames user files and applies mutate to one system
// namespace in the same transaction. Like DeleteWithSystemState, it refuses a
// rename that changes a DefaultProject file through an overlaid project.
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
	return s.rename(userCtx, auth, req, func(ctx context.Context, tx *sql.Tx, change RenameStateChange) error {
		if err := requireAddressedProject(project, change); err != nil {
			return err
		}
		return s.mutateRenameSystemStateTx(ctx, tx, systemProject, owner, mutate)
	})
}

// requireAddressedProject refuses a change outside the one project a
// single-namespace state handle is bound to.
func requireAddressedProject(project string, change RenameStateChange) error {
	if change.Project != project || change.TargetProject != project {
		return NewError(ErrCodeInvalidArgument, "the mutation changes "+DefaultProject+" files; this state handle is bound to one project", false)
	}
	return nil
}

// RenameStateChange describes one physical part of a mutation: the exact file
// movements from Project into TargetProject and the live TargetProject paths it
// removed, whether replaced by a rename or deleted. Project and TargetProject
// are physical user projects and are equal within one project. A mutation of
// an overlaid project can report a DefaultProject change, so system state must
// follow these projects, never the addressed one.
type RenameStateChange struct {
	Project, TargetProject string
	Mappings               []RenameMapping
	OverwrittenPaths       []string
}

// SystemStateMoveMutator updates the source and target state in the rename
// transaction. A nil target means a same-namespace change. Existing byte
// slices are read-only; replace map values rather than mutating them in place.
type SystemStateMoveMutator func(change RenameStateChange, source, target map[string][]byte) error

// SystemStateMovePlan bounds the read set for one plugin state change.
// SystemProject maps a physical user project to its system namespace.
// CatalogPaths are loaded from each namespace first. AffectedPaths selects the
// additional data needed for the exact FileIO changes, without reading
// unrelated bodies.
type SystemStateMovePlan struct {
	SystemProject func(userProject string) string
	CatalogPaths  []string
	AffectedPaths func(change RenameStateChange, sourceCatalog, targetCatalog map[string][]byte) (source, target []string, err error)
	Mutate        SystemStateMoveMutator
}

// validate rejects an incomplete plan before any user file changes.
func (p SystemStateMovePlan) validate() error {
	if p.SystemProject == nil || p.Mutate == nil || p.AffectedPaths == nil {
		return errors.New("system state move plan is incomplete")
	}
	return nil
}

// RenameMappedWithSystemState applies the file move and the corresponding
// state change of every physical project pair it changed, under the
// participating project locks, in one transaction.
func (s *Service) RenameMappedWithSystemState(
	ctx context.Context,
	auth AuthContext,
	project, fromPath, targetProject, toPath string,
	overwrite bool,
	plan SystemStateMovePlan,
) (RenameResult, error) {
	if err := plan.validate(); err != nil {
		return RenameResult{}, errors.WithStack(err)
	}
	owner := systemOwnerFromContext(ctx)
	if owner == "" {
		return RenameResult{}, errors.New("system_owner is required")
	}
	if targetProject == "" {
		targetProject = project
	}
	userCtx := contextWithSystemOwner(ctx, "")
	req := renameRequest{project: project, fromPath: fromPath, targetProject: targetProject, toPath: toPath, overwrite: overwrite}
	return s.rename(userCtx, auth, req, func(ctx context.Context, tx *sql.Tx, change RenameStateChange) error {
		return s.mutateMoveSystemStateTx(ctx, tx, owner, change, plan)
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
