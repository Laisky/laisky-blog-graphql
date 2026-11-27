package files

import (
	"cmp"
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// renameSourceFile represents an active file selected as a rename source.
type renameSourceFile struct {
	ID           uint64
	Path         string
	Size         int64
	SkipRAGIndex bool
	ContentHash  string
}

// RenameMapping represents one old path to new path update during rename.
type RenameMapping struct {
	ID           uint64
	OldPath      string
	NewPath      string
	SkipRAGIndex bool
	ContentHash  string
}

// RenameRequest describes one rename or move of a file or directory subtree.
// An empty TargetProject, or one equal to Project, renames inside Project.
// Any other TargetProject moves the files into that project of the same
// caller, where ToPath, overwrite and destination conditions apply.
type RenameRequest struct {
	Project       string
	FromPath      string
	TargetProject string
	ToPath        string
	Overwrite     bool
}

// Destination returns the project that receives the renamed files.
func (r RenameRequest) Destination() string {
	if r.TargetProject == "" {
		return r.Project
	}
	return r.TargetProject
}

// crossProject reports whether the request moves files into another project.
func (r RenameRequest) crossProject() bool {
	return r.Destination() != r.Project
}

// RenameStateChange describes the exact file movements and displaced live
// paths of one rename transaction. Derived state must follow these paths.
type RenameStateChange struct {
	Mappings         []RenameMapping
	OverwrittenPaths []string
}

// renameStateTx persists plugin-owned system state inside the rename transaction.
type renameStateTx func(ctx context.Context, tx *sql.Tx, change RenameStateChange) error

// Rename renames or moves a file path or directory subtree, within one project
// or into another project of the same caller, in one transaction. File
// identity, version history and summary metadata travel with the rows; moved
// bytes must fit the destination project's quota.
func (s *Service) Rename(ctx context.Context, auth AuthContext, req RenameRequest) (RenameResult, error) {
	return s.rename(ctx, auth, req, nil)
}

// rename validates and applies one rename request. It is the only rename
// implementation; mutateState, when non-nil, persists derived system state in
// the same transaction after the user rows moved.
func (s *Service) rename(ctx context.Context, auth AuthContext, req RenameRequest, mutateState renameStateTx) (RenameResult, error) {
	req.TargetProject = req.Destination()
	if err := s.validateRenameRequest(auth, req); err != nil {
		return RenameResult{}, errors.WithStack(err)
	}
	conditions := filePreconditionsFromContext(ctx, auth, req.Project, req.FromPath, FileOperationRename)
	samePath := !req.crossProject() && req.FromPath == req.ToPath
	if samePath && conditions.Empty() {
		return RenameResult{MovedCount: 0}, nil
	}
	movedCount := 0
	err := s.withProjectLocks(ctx, auth.APIKeyHash, []string{req.Project, req.TargetProject}, func(tx *sql.Tx) error {
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
			if err := mutateState(ctx, tx, moved); err != nil {
				return err
			}
		}
		movedCount = len(moved.Mappings)
		return nil
	})
	if err != nil {
		return RenameResult{}, errors.WithStack(err)
	}
	return RenameResult{MovedCount: movedCount}, nil
}

// validateRenameRequest checks the caller, both projects and both paths before
// any lock or storage access.
func (s *Service) validateRenameRequest(auth AuthContext, req RenameRequest) error {
	if err := s.validateAuth(auth); err != nil {
		return err
	}
	if err := ValidateProject(req.Project); err != nil {
		return err
	}
	if err := ValidateTargetProject(req.TargetProject); err != nil {
		return err
	}
	if err := ValidatePath(req.FromPath); err != nil {
		return err
	}
	if err := ValidatePath(req.ToPath); err != nil {
		return err
	}
	if req.FromPath == "" || req.ToPath == "" {
		return NewError(ErrCodeInvalidPath, "source and destination paths must be non-root", false)
	}
	return nil
}

// applyRenameTx resolves the sources, validates the destination in the target
// project and moves the rows, returning how many files moved.
func (s *Service) applyRenameTx(ctx context.Context, tx *sql.Tx, auth AuthContext, req RenameRequest) (RenameStateChange, error) {
	owner := systemOwnerFromContext(ctx)
	sourceFiles, sourceIsDirectory, err := s.resolveRenameSources(ctx, tx, auth.APIKeyHash, req.Project, req.FromPath)
	if err != nil {
		return RenameStateChange{}, err
	}
	if !req.crossProject() && sourceIsDirectory && strings.HasPrefix(req.ToPath, req.FromPath+"/") {
		return RenameStateChange{}, NewError(ErrCodeInvalidPath, "destination cannot be within source subtree", false)
	}
	if err := s.ensureNoParentFile(ctx, tx, auth.APIKeyHash, req.TargetProject, req.ToPath); err != nil {
		return RenameStateChange{}, err
	}
	mappings, err := buildRenameMappings(sourceFiles, req.FromPath, req.ToPath, sourceIsDirectory)
	if err != nil {
		return RenameStateChange{}, err
	}
	if sourceIsDirectory {
		// Directories created below to_path must not be files of the destination.
		if err := s.ensureNoFileAt(ctx, tx, auth.APIKeyHash, req.TargetProject, renameInnerDirectories(mappings, req.ToPath, req.crossProject())); err != nil {
			return RenameStateChange{}, err
		}
	}
	now := s.clock()
	overwritePaths, err := s.validateRenameDestinations(ctx, tx, auth.APIKeyHash, req.TargetProject, mappings, req.ToPath, sourceIsDirectory, req.Overwrite)
	if err != nil {
		return RenameStateChange{}, err
	}
	if err := s.softDeleteRenameDestinationsTx(ctx, tx, auth, req.TargetProject, owner, overwritePaths, now); err != nil {
		return RenameStateChange{}, err
	}
	if req.crossProject() {
		// Overwritten destinations are already released, so the quota sees the
		// target project exactly as it will be after the move commits.
		if err := s.ensureProjectQuota(ctx, tx, auth.APIKeyHash, req.TargetProject, renameSourceBytes(sourceFiles), nil); err != nil {
			return RenameStateChange{}, err
		}
	}
	if err := s.applyRenameMappingsTx(ctx, tx, auth, req, owner, mappings, now); err != nil {
		return RenameStateChange{}, err
	}
	return RenameStateChange{Mappings: mappings, OverwrittenPaths: overwritePaths}, nil
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
func (s *Service) checkRenamePreconditionsTx(ctx context.Context, tx *sql.Tx, auth AuthContext, req RenameRequest, p FilePreconditions) error {
	if p.Empty() {
		return nil
	}
	if p.DestinationPath != "" {
		destinationProject := p.DestinationProject
		if destinationProject == "" {
			destinationProject = req.Project
		}
		if p.DestinationPath != req.ToPath || destinationProject != req.TargetProject {
			return NewError(ErrCodeInvalidArgument, "rename destination does not match its precondition scope", false)
		}
	}
	if err := s.checkPathVersionTx(ctx, tx, auth, req.Project, req.FromPath, p.ExpectedVersion, false); err != nil {
		return err
	}
	if !req.crossProject() && req.FromPath == req.ToPath {
		return s.checkPathVersionTx(ctx, tx, auth, req.TargetProject, req.ToPath, p.ExpectedDestinationVersion, p.DestinationMustNotExist)
	}
	if req.Overwrite && p.ExpectedDestinationVersion == "" && !p.DestinationMustNotExist {
		return NewError(ErrCodeInvalidArgument, "conditional overwrite rename requires a destination version or destination_must_not_exist", false)
	}
	if !req.Overwrite && p.ExpectedDestinationVersion != "" {
		return NewError(ErrCodeInvalidArgument, "expected_destination_version requires overwrite=true", false)
	}
	return s.checkPathVersionTx(ctx, tx, auth, req.TargetProject, req.ToPath, p.ExpectedDestinationVersion, p.DestinationMustNotExist || !req.Overwrite)
}

// resolveRenameSources resolves source files for a file or directory rename.
func (s *Service) resolveRenameSources(ctx context.Context, tx *sql.Tx, apiKeyHash, project, fromPath string) ([]renameSourceFile, bool, error) {
	owner := systemOwnerFromContext(ctx)
	var exact renameSourceFile
	err := tx.QueryRowContext(ctx,
		rebindSQL(`SELECT id, path, size, skip_rag_index, content_hash FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path = ? AND deleted = FALSE AND system_owner = ? LIMIT 1`, s.isPostgres),
		apiKeyHash, project, fromPath, owner,
	).Scan(&exact.ID, &exact.Path, &exact.Size, &exact.SkipRAGIndex, &exact.ContentHash)
	if err == nil {
		return []renameSourceFile{exact}, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, errors.Wrap(err, "query rename source file")
	}
	prefix := buildPathPrefix(fromPath)
	rows, err := tx.QueryContext(ctx,
		rebindSQL(`SELECT id, path, size, skip_rag_index, content_hash FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path LIKE ? AND deleted = FALSE AND system_owner = ? ORDER BY path ASC`, s.isPostgres),
		apiKeyHash, project, prefix, owner,
	)
	if err != nil {
		return nil, false, errors.Wrap(err, "query rename source descendants")
	}
	defer func() { _ = rows.Close() }()
	var descendants []renameSourceFile
	for rows.Next() {
		var row renameSourceFile
		if scanErr := rows.Scan(&row.ID, &row.Path, &row.Size, &row.SkipRAGIndex, &row.ContentHash); scanErr != nil {
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

// renameInnerDirectories returns the directories that a directory move creates
// strictly below toPath, each once. Within one project, the old path of a moved
// file is free after the move, so it is not checked.
func renameInnerDirectories(mappings []RenameMapping, toPath string, crossProject bool) []string {
	seen := make(map[string]struct{})
	if !crossProject {
		for _, mapping := range mappings {
			seen[mapping.OldPath] = struct{}{}
		}
	}
	var directories []string
	for _, mapping := range mappings {
		for _, parent := range parentPaths(mapping.NewPath) {
			if _, ok := seen[parent]; ok || !strings.HasPrefix(parent, toPath+"/") {
				continue
			}
			seen[parent] = struct{}{}
			directories = append(directories, parent)
		}
	}
	return directories
}

// buildRenameMappings computes destination paths for the selected rename
// source files. Mappings are ordered by ascending source path length: when a
// directory moves into its own parent, a new path can equal another moved
// file's old path, and that file is always the shorter one, so it moves first
// and the path is free before it is reused.
func buildRenameMappings(sourceFiles []renameSourceFile, fromPath, toPath string, sourceIsDirectory bool) ([]RenameMapping, error) {
	mappings := make([]RenameMapping, 0, len(sourceFiles))
	for _, source := range sourceFiles {
		newPath := toPath
		if sourceIsDirectory {
			suffix := strings.TrimPrefix(source.Path, fromPath)
			if !strings.HasPrefix(source.Path, fromPath+"/") || suffix == source.Path {
				return nil, NewError(ErrCodeInvalidPath, "invalid directory rename source mapping", false)
			}
			newPath = toPath + suffix
		}
		mappings = append(mappings, RenameMapping{ID: source.ID, OldPath: source.Path, NewPath: newPath, SkipRAGIndex: source.SkipRAGIndex, ContentHash: source.ContentHash})
	}
	slices.SortStableFunc(mappings, func(a, b RenameMapping) int { return cmp.Compare(len(a.OldPath), len(b.OldPath)) })
	return mappings, nil
}

// validateRenameDestinations checks rename collisions and returns overwrite targets.
func (s *Service) validateRenameDestinations(
	ctx context.Context, tx *sql.Tx, apiKeyHash, project string, mappings []RenameMapping,
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
	req RenameRequest, owner string, mappings []RenameMapping, now time.Time,
) error {
	for _, mapping := range mappings {
		if _, err := tx.ExecContext(ctx,
			rebindSQL(`UPDATE mcp_files SET project = ?, path = ?, updated_at = ? WHERE id = ? AND system_owner = ?`, s.isPostgres),
			req.TargetProject, mapping.NewPath, now, mapping.ID, owner,
		); err != nil {
			return errors.Wrap(err, "apply rename path remap")
		}
		if _, err := tx.ExecContext(ctx,
			rebindSQL(`UPDATE mcp_file_versions SET project = ?, path = ? WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = ?`, s.isPostgres),
			req.TargetProject, mapping.NewPath, auth.APIKeyHash, req.Project, mapping.OldPath, owner,
		); err != nil {
			return errors.Wrap(err, "apply rename version path remap")
		}
		if owner == "" {
			if err := s.insertIndexJobTx(ctx, tx, FileIndexJob{
				APIKeyHash: auth.APIKeyHash, Project: req.Project, FilePath: mapping.OldPath,
				Operation: indexOperationDelete, FileUpdatedAt: &now, Status: indexStatusPending, RetryCount: 0,
				AvailableAt: now, CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				return errors.Wrap(err, "enqueue rename delete job")
			}
			if mapping.SkipRAGIndex {
				// This destination will not get an upsert. Clear old RAG rows
				// atomically, also covering a path reused at the same timestamp.
				if err := s.deleteIndexRowsTx(ctx, tx, auth.APIKeyHash, req.TargetProject, mapping.NewPath); err != nil {
					return err
				}
				continue
			}
			if err := s.insertIndexJobTx(ctx, tx, FileIndexJob{
				APIKeyHash: auth.APIKeyHash, Project: req.TargetProject, FilePath: mapping.NewPath,
				Operation: indexOperationUpsert, FileUpdatedAt: &now, ContentHash: mapping.ContentHash, Status: indexStatusPending, RetryCount: 0,
				AvailableAt: now, CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				return errors.Wrap(err, "enqueue rename upsert job")
			}
			if err := s.storeCredentialEnvelopeTx(ctx, tx, auth, req.TargetProject, mapping.NewPath, now); err != nil {
				return err
			}
		}
	}

	return nil
}
