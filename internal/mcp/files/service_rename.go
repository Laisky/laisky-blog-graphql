package files

import (
	"context"
	"database/sql"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// renameSourceFile represents an active file selected as a rename source.
type renameSourceFile struct {
	ID   uint64
	Path string
	Size int64
}

// renameMapping represents one old path to new path update during rename.
type renameMapping struct {
	ID      uint64
	OldPath string
	NewPath string
}

// renameRequest describes one rename or move. targetProject equals project for
// an ordinary rename and names another project of the same caller for a
// cross-project move.
type renameRequest struct {
	project, fromPath     string
	targetProject, toPath string
	overwrite             bool
}

// crossProject reports whether the request moves files into another project.
func (r renameRequest) crossProject() bool {
	return r.targetProject != r.project
}

// renameStateTx persists plugin-owned system state inside the rename transaction.
type renameStateTx func(ctx context.Context, tx *sql.Tx) error

// Rename renames or moves a file path or directory subtree within one project.
func (s *Service) Rename(ctx context.Context, auth AuthContext, project, fromPath, toPath string, overwrite bool) (RenameResult, error) {
	return s.rename(ctx, auth, renameRequest{project: project, fromPath: fromPath, targetProject: project, toPath: toPath, overwrite: overwrite}, nil)
}

// RenameAcrossProjects moves a file or directory subtree from project into
// targetProject of the same caller in one transaction. File identity, version
// history and summary metadata travel with the rows. The destination follows the
// same collision, overwrite and precondition rules as a same-project rename,
// evaluated in targetProject, and the moved bytes must fit targetProject's quota.
// An empty targetProject keeps the move inside project.
func (s *Service) RenameAcrossProjects(ctx context.Context, auth AuthContext, project, fromPath, targetProject, toPath string, overwrite bool) (RenameResult, error) {
	if targetProject == "" {
		targetProject = project
	}
	return s.rename(ctx, auth, renameRequest{project: project, fromPath: fromPath, targetProject: targetProject, toPath: toPath, overwrite: overwrite}, nil)
}

// rename validates and applies one rename request. mutateState, when non-nil,
// persists plugin-owned system state in the same transaction.
func (s *Service) rename(ctx context.Context, auth AuthContext, req renameRequest, mutateState renameStateTx) (RenameResult, error) {
	if err := s.validateRenameRequest(auth, req); err != nil {
		return RenameResult{}, errors.WithStack(err)
	}
	conditions := filePreconditionsFromContext(ctx, auth, req.project, req.fromPath, FileOperationRename)
	samePath := !req.crossProject() && req.fromPath == req.toPath
	if samePath && conditions.Empty() {
		return RenameResult{MovedCount: 0}, nil
	}
	movedCount := 0
	err := s.withProjectLocks(ctx, auth.APIKeyHash, []string{req.project, req.targetProject}, func(tx *sql.Tx) error {
		if err := s.checkRenamePreconditionsTx(ctx, tx, auth, req, conditions); err != nil {
			return err
		}
		// A conditional no-op must still validate its source, but must not bump revision.
		if samePath {
			return nil
		}
		moved, err := s.applyRenameTx(ctx, tx, auth, req)
		if err != nil {
			return err
		}
		if mutateState != nil {
			if err := mutateState(ctx, tx); err != nil {
				return err
			}
		}
		movedCount = moved
		return nil
	})
	if err != nil {
		return RenameResult{}, errors.WithStack(err)
	}
	return RenameResult{MovedCount: movedCount}, nil
}

// validateRenameRequest checks the caller, both projects and both paths before
// any lock or storage access.
func (s *Service) validateRenameRequest(auth AuthContext, req renameRequest) error {
	if err := s.validateAuth(auth); err != nil {
		return err
	}
	if err := ValidateProject(req.project); err != nil {
		return err
	}
	if err := ValidateTargetProject(req.targetProject); err != nil {
		return err
	}
	if err := ValidatePath(req.fromPath); err != nil {
		return err
	}
	if err := ValidatePath(req.toPath); err != nil {
		return err
	}
	if req.fromPath == "" || req.toPath == "" {
		return NewError(ErrCodeInvalidPath, "source and destination paths must be non-root", false)
	}
	return nil
}

// applyRenameTx resolves the sources, validates the destination in the target
// project and moves the rows, returning how many files moved.
func (s *Service) applyRenameTx(ctx context.Context, tx *sql.Tx, auth AuthContext, req renameRequest) (int, error) {
	owner := systemOwnerFromContext(ctx)
	sourceFiles, sourceIsDirectory, err := s.resolveRenameSources(ctx, tx, auth.APIKeyHash, req.project, req.fromPath)
	if err != nil {
		return 0, err
	}
	if !req.crossProject() && sourceIsDirectory && strings.HasPrefix(req.toPath, req.fromPath+"/") {
		return 0, NewError(ErrCodeInvalidPath, "destination cannot be within source subtree", false)
	}
	if err := s.ensureNoParentFile(ctx, tx, auth.APIKeyHash, req.targetProject, req.toPath); err != nil {
		return 0, err
	}
	mappings, err := buildRenameMappings(sourceFiles, req.fromPath, req.toPath, sourceIsDirectory)
	if err != nil {
		return 0, err
	}
	now := s.clock()
	overwritePaths, err := s.validateRenameDestinations(ctx, tx, auth.APIKeyHash, req.targetProject, mappings, req.toPath, sourceIsDirectory, req.overwrite)
	if err != nil {
		return 0, err
	}
	if err := s.softDeleteRenameDestinationsTx(ctx, tx, auth, req.targetProject, owner, overwritePaths, now); err != nil {
		return 0, err
	}
	if req.crossProject() {
		// Overwritten destinations are already released, so the quota sees the
		// target project exactly as it will be after the move commits.
		if err := s.ensureProjectQuota(ctx, tx, auth.APIKeyHash, req.targetProject, renameSourceBytes(sourceFiles), nil); err != nil {
			return 0, err
		}
	}
	if err := s.applyRenameMappingsTx(ctx, tx, auth, req, owner, mappings, now); err != nil {
		return 0, err
	}
	if err := s.enqueueRenameOverwriteDeletesTx(ctx, tx, auth, req.targetProject, owner, overwritePaths, now); err != nil {
		return 0, err
	}
	return len(mappings), nil
}

// ValidateTargetProject validates a rename destination project and names it in
// the error, so a caller can tell it apart from the source project.
func ValidateTargetProject(project string) error {
	err := ValidateProject(project)
	if typed, ok := AsError(err); ok {
		return NewError(typed.Code, "target "+typed.Message, typed.Retryable)
	}
	return err
}

// renameSourceBytes returns the total stored size of the selected sources.
func renameSourceBytes(sources []renameSourceFile) int64 {
	var total int64
	for _, source := range sources {
		total += source.Size
	}
	return total
}

// withProjectLocks runs fn in one transaction holding every distinct project
// scope. A single scope uses the configured LockProvider; several scopes
// require a MultiProjectLockProvider, so a provider that cannot hold them all
// refuses the mutation rather than leaving one project unserialized.
func (s *Service) withProjectLocks(ctx context.Context, apiKeyHash string, projects []string, fn func(tx *sql.Tx) error) error {
	ordered := ProjectLockOrder(apiKeyHash, projects...)
	if len(ordered) == 1 {
		return s.lockProvider.WithProjectLock(ctx, s.db, s.isPostgres, apiKeyHash, ordered[0], s.settings.LockTimeout, fn)
	}
	multi, ok := s.lockProvider.(MultiProjectLockProvider)
	if !ok {
		return errors.WithStack(NewError(ErrCodeSearchBackend, "configured lock provider cannot serialize a cross-project mutation", false))
	}
	return multi.WithProjectsLock(ctx, s.db, s.isPostgres, apiKeyHash, ordered, s.settings.LockTimeout, fn)
}

// checkRenamePreconditionsTx checks both names before any lifecycle side effect.
func (s *Service) checkRenamePreconditionsTx(ctx context.Context, tx *sql.Tx, auth AuthContext, req renameRequest, p FilePreconditions) error {
	if p.Empty() {
		return nil
	}
	if p.DestinationPath != "" {
		destinationProject := p.DestinationProject
		if destinationProject == "" {
			destinationProject = req.project
		}
		if p.DestinationPath != req.toPath || destinationProject != req.targetProject {
			return NewError(ErrCodeInvalidArgument, "rename destination does not match its precondition scope", false)
		}
	}
	if err := s.checkPathVersionTx(ctx, tx, auth, req.project, req.fromPath, p.ExpectedVersion, false); err != nil {
		return err
	}
	if !req.crossProject() && req.fromPath == req.toPath {
		return s.checkPathVersionTx(ctx, tx, auth, req.targetProject, req.toPath, p.ExpectedDestinationVersion, p.DestinationMustNotExist)
	}
	if req.overwrite && p.ExpectedDestinationVersion == "" && !p.DestinationMustNotExist {
		return NewError(ErrCodeInvalidArgument, "conditional overwrite rename requires a destination version or destination_must_not_exist", false)
	}
	if !req.overwrite && p.ExpectedDestinationVersion != "" {
		return NewError(ErrCodeInvalidArgument, "expected_destination_version requires overwrite=true", false)
	}
	return s.checkPathVersionTx(ctx, tx, auth, req.targetProject, req.toPath, p.ExpectedDestinationVersion, p.DestinationMustNotExist || !req.overwrite)
}

// resolveRenameSources resolves source files for a file or directory rename.
func (s *Service) resolveRenameSources(ctx context.Context, tx *sql.Tx, apiKeyHash, project, fromPath string) ([]renameSourceFile, bool, error) {
	owner := systemOwnerFromContext(ctx)
	var exact renameSourceFile
	err := tx.QueryRowContext(ctx,
		rebindSQL(`SELECT id, path, size FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path = ? AND deleted = FALSE AND system_owner = ? LIMIT 1`, s.isPostgres),
		apiKeyHash, project, fromPath, owner,
	).Scan(&exact.ID, &exact.Path, &exact.Size)
	if err == nil {
		return []renameSourceFile{exact}, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, errors.Wrap(err, "query rename source file")
	}
	prefix := buildPathPrefix(fromPath)
	rows, err := tx.QueryContext(ctx,
		rebindSQL(`SELECT id, path, size FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path LIKE ? AND deleted = FALSE AND system_owner = ? ORDER BY path ASC`, s.isPostgres),
		apiKeyHash, project, prefix, owner,
	)
	if err != nil {
		return nil, false, errors.Wrap(err, "query rename source descendants")
	}
	defer func() { _ = rows.Close() }()
	var descendants []renameSourceFile
	for rows.Next() {
		var row renameSourceFile
		if scanErr := rows.Scan(&row.ID, &row.Path, &row.Size); scanErr != nil {
			return nil, false, errors.Wrap(scanErr, "scan rename source descendant")
		}
		descendants = append(descendants, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, errors.Wrap(err, "iterate rename source descendants")
	}
	if len(descendants) == 0 {
		return nil, false, NewError(ErrCodeNotFound, "source path not found", false)
	}
	return descendants, true, nil
}

// buildRenameMappings computes destination paths for the selected rename source files.
func buildRenameMappings(sourceFiles []renameSourceFile, fromPath, toPath string, sourceIsDirectory bool) ([]renameMapping, error) {
	mappings := make([]renameMapping, 0, len(sourceFiles))
	for _, source := range sourceFiles {
		newPath := toPath
		if sourceIsDirectory {
			suffix := strings.TrimPrefix(source.Path, fromPath)
			if !strings.HasPrefix(source.Path, fromPath+"/") || suffix == source.Path {
				return nil, NewError(ErrCodeInvalidPath, "invalid directory rename source mapping", false)
			}
			newPath = toPath + suffix
		}
		mappings = append(mappings, renameMapping{ID: source.ID, OldPath: source.Path, NewPath: newPath})
	}
	return mappings, nil
}

// validateRenameDestinations checks rename collisions and returns overwrite targets.
func (s *Service) validateRenameDestinations(
	ctx context.Context, tx *sql.Tx, apiKeyHash, project string, mappings []renameMapping,
	toPath string, sourceIsDirectory, overwrite bool,
) ([]string, error) {
	destinationPaths := make([]string, 0, len(mappings))
	sourcePathByID := make(map[uint64]string, len(mappings))
	pathSet := make(map[string]struct{}, len(mappings))
	for _, mapping := range mappings {
		sourcePathByID[mapping.ID] = mapping.OldPath
		if _, exists := pathSet[mapping.NewPath]; exists {
			return nil, NewError(ErrCodeAlreadyExists, "destination path collision", false)
		}
		pathSet[mapping.NewPath] = struct{}{}
		destinationPaths = append(destinationPaths, mapping.NewPath)
	}
	owner := systemOwnerFromContext(ctx)
	if !sourceIsDirectory {
		var descendantCount int64
		if err := tx.QueryRowContext(ctx,
			rebindSQL(`SELECT COUNT(1) FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path LIKE ? AND deleted = FALSE AND system_owner = ?`, s.isPostgres),
			apiKeyHash, project, buildPathPrefix(toPath), owner,
		).Scan(&descendantCount); err != nil {
			return nil, errors.Wrap(err, "check destination descendants")
		}
		if descendantCount > 0 {
			return nil, NewError(ErrCodeAlreadyExists, "destination path already exists", false)
		}
	}
	if sourceIsDirectory {
		var destinationRootFileCount int64
		if err := tx.QueryRowContext(ctx,
			rebindSQL(`SELECT COUNT(1) FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path = ? AND deleted = FALSE AND system_owner = ?`, s.isPostgres),
			apiKeyHash, project, toPath, owner,
		).Scan(&destinationRootFileCount); err != nil {
			return nil, errors.Wrap(err, "check destination root collision")
		}
		if destinationRootFileCount > 0 {
			return nil, NewError(ErrCodeAlreadyExists, "destination path already exists", false)
		}
	}
	inClause, inArgs := buildInClause(destinationPaths, s.isPostgres, 4)
	query := rebindSQL(`SELECT id, path FROM mcp_files WHERE apikey_hash = ? AND project = ? AND deleted = FALSE AND system_owner = ? AND path IN (%s)`, s.isPostgres)
	args := make([]any, 0, 3+len(inArgs))
	args = append(args, apiKeyHash, project, owner)
	args = append(args, inArgs...)
	rows, err := tx.QueryContext(ctx, strings.Replace(query, "%s", inClause, 1), args...)
	if err != nil {
		return nil, errors.Wrap(err, "query destination collisions")
	}
	defer func() { _ = rows.Close() }()
	var destinationFiles []renameSourceFile
	for rows.Next() {
		var row renameSourceFile
		if scanErr := rows.Scan(&row.ID, &row.Path); scanErr != nil {
			return nil, errors.Wrap(scanErr, "scan destination collision")
		}
		destinationFiles = append(destinationFiles, row)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate destination collisions")
	}
	overwritePaths := make([]string, 0, len(destinationFiles))
	for _, destination := range destinationFiles {
		if sourcePath, ok := sourcePathByID[destination.ID]; ok && sourcePath == destination.Path {
			continue
		}
		if !overwrite || sourceIsDirectory {
			return nil, NewError(ErrCodeAlreadyExists, "destination path already exists", false)
		}
		overwritePaths = append(overwritePaths, destination.Path)
	}
	return overwritePaths, nil
}

// softDeleteRenameDestinationsTx frees overwritten names before applying the remap.
func (s *Service) softDeleteRenameDestinationsTx(ctx context.Context, tx *sql.Tx, auth AuthContext,
	project, owner string, overwritePaths []string, now time.Time,
) error {
	if len(overwritePaths) > 0 {
		inClause, inArgs := buildInClause(overwritePaths, s.isPostgres, 6)
		query := rebindSQL(`UPDATE mcp_files SET deleted = TRUE, deleted_at = ?, updated_at = ?
    WHERE apikey_hash = ? AND project = ? AND deleted = FALSE AND system_owner = ? AND path IN (%s)`, s.isPostgres)
		args := make([]any, 0, 5+len(inArgs))
		args = append(args, now, now, auth.APIKeyHash, project, owner)
		args = append(args, inArgs...)
		if _, err := tx.ExecContext(ctx, strings.Replace(query, "%s", inClause, 1), args...); err != nil {
			return errors.Wrap(err, "soft delete overwritten destination files")
		}
	}

	return nil
}

// applyRenameMappingsTx moves file/version rows into the destination project
// and path, and schedules their index updates atomically. Index rows are keyed by
// project, so a cross-project move deletes the old index entry in the source
// project and rebuilds the new one in the target project.
func (s *Service) applyRenameMappingsTx(ctx context.Context, tx *sql.Tx, auth AuthContext,
	req renameRequest, owner string, mappings []renameMapping, now time.Time,
) error {
	for _, mapping := range mappings {
		if _, err := tx.ExecContext(ctx,
			rebindSQL(`UPDATE mcp_files SET project = ?, path = ?, updated_at = ? WHERE id = ? AND system_owner = ?`, s.isPostgres),
			req.targetProject, mapping.NewPath, now, mapping.ID, owner,
		); err != nil {
			return errors.Wrap(err, "apply rename path remap")
		}
		if _, err := tx.ExecContext(ctx,
			rebindSQL(`UPDATE mcp_file_versions SET project = ?, path = ? WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = ?`, s.isPostgres),
			req.targetProject, mapping.NewPath, auth.APIKeyHash, req.project, mapping.OldPath, owner,
		); err != nil {
			return errors.Wrap(err, "apply rename version path remap")
		}
		if owner == "" {
			if err := s.insertIndexJobTx(ctx, tx, FileIndexJob{
				APIKeyHash: auth.APIKeyHash, Project: req.project, FilePath: mapping.OldPath,
				Operation: indexOperationDelete, FileUpdatedAt: &now, Status: indexStatusPending, RetryCount: 0,
				AvailableAt: now, CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				return errors.Wrap(err, "enqueue rename delete job")
			}
			if err := s.insertIndexJobTx(ctx, tx, FileIndexJob{
				APIKeyHash: auth.APIKeyHash, Project: req.targetProject, FilePath: mapping.NewPath,
				Operation: indexOperationUpsert, FileUpdatedAt: &now, Status: indexStatusPending, RetryCount: 0,
				AvailableAt: now, CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				return errors.Wrap(err, "enqueue rename upsert job")
			}
			if err := s.storeCredentialEnvelopeTx(ctx, tx, auth, req.targetProject, mapping.NewPath, now); err != nil {
				return err
			}
		}
	}

	return nil
}

// mutateRenameSystemStateTx persists the plugin manifest in the same rename transaction.
func (s *Service) mutateRenameSystemStateTx(ctx context.Context, tx *sql.Tx,
	systemProject, systemOwner string, mutate SystemStateMutator,
) error {
	stateCtx := contextWithSystemOwner(ctx, systemOwner)
	state, err := s.loadSystemStateTx(stateCtx, tx, systemOwner, systemProject)
	if err != nil {
		return err
	}
	if err := mutate(state); err != nil {
		return errors.Wrap(err, "mutate rename system state")
	}
	return s.persistSystemStateTx(stateCtx, tx, systemOwner, systemProject, state)
}

// mutateMoveSystemStateTx loads the source and target namespaces of one system
// owner, lets the mutator move entries between them, and persists both in the
// cross-project rename transaction.
func (s *Service) mutateMoveSystemStateTx(ctx context.Context, tx *sql.Tx,
	systemOwner, systemProject, targetSystemProject string, mutate SystemStateMoveMutator,
) error {
	stateCtx := contextWithSystemOwner(ctx, systemOwner)
	source, err := s.loadSystemStateTx(stateCtx, tx, systemOwner, systemProject)
	if err != nil {
		return err
	}
	target, err := s.loadSystemStateTx(stateCtx, tx, systemOwner, targetSystemProject)
	if err != nil {
		return err
	}
	if err := mutate(source, target); err != nil {
		return errors.Wrap(err, "mutate cross-project rename system state")
	}
	if err := s.persistSystemStateTx(stateCtx, tx, systemOwner, systemProject, source); err != nil {
		return err
	}
	return s.persistSystemStateTx(stateCtx, tx, systemOwner, targetSystemProject, target)
}

// enqueueRenameOverwriteDeletesTx schedules deleted destinations in the same rename transaction.
func (s *Service) enqueueRenameOverwriteDeletesTx(ctx context.Context, tx *sql.Tx, auth AuthContext,
	project, owner string, overwritePaths []string, now time.Time,
) error {
	if owner == "" {
		for _, overwrittenPath := range overwritePaths {
			if err := s.insertIndexJobTx(ctx, tx, FileIndexJob{
				APIKeyHash: auth.APIKeyHash, Project: project, FilePath: overwrittenPath,
				Operation: indexOperationDelete, FileUpdatedAt: &now, Status: indexStatusPending, RetryCount: 0,
				AvailableAt: now, CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				return errors.Wrap(err, "enqueue overwrite delete job")
			}
		}
	}
	return nil
}
