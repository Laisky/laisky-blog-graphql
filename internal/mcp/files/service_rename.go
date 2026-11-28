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

// Rename renames or moves a file path or directory subtree, within one project
// or into another project of the same caller, in one transaction. File
// identity, version history and summary metadata travel with the rows; moved
// bytes must fit the destination project's quota. It is the only rename
// implementation and never depends on a search index: the RAG outbox
// re-indexes the moved paths after commit.
func (s *Service) Rename(ctx context.Context, auth AuthContext, req RenameRequest) (RenameResult, error) {
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
		var err error
		movedCount, err = s.applyRenameTx(ctx, tx, auth, req)
		return err
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

// renameMove is one validated physical move: the source rows of req.Project,
// their destination paths in req.TargetProject, and the one destination file a
// file rename replaces.
type renameMove struct {
	req         RenameRequest
	sources     []renameSourceFile
	mappings    []RenameMapping
	overwritten []string
}

// applyRenameTx resolves the sources, validates the destination in the target
// project and moves the rows, returning how many files moved. Every check runs
// before the first change, so a rejected rename leaves rows, history and the
// index outbox untouched.
func (s *Service) applyRenameTx(ctx context.Context, tx *sql.Tx, auth AuthContext, req RenameRequest) (int, error) {
	sources, sourceIsDirectory, err := s.resolveRenameSources(ctx, tx, auth.APIKeyHash, req.Project, req.FromPath)
	if err != nil {
		return 0, err
	}
	move, err := s.planRenameMoveTx(ctx, tx, auth.APIKeyHash, req, sources, sourceIsDirectory)
	if err != nil {
		return 0, err
	}
	return s.applyRenameMoveTx(ctx, tx, auth, move)
}

// planRenameMoveTx validates moving sources, the rows of req.Project that
// req.FromPath selects, to req.ToPath in req.TargetProject, and computes their
// new paths. It changes nothing. A file keeps the ordinary contract checked by
// checkFileDestinationTx; a directory is relocated, never merged or replaced:
// overwrite is rejected and the destination must be vacant.
func (s *Service) planRenameMoveTx(ctx context.Context, tx *sql.Tx, apiKeyHash string, req RenameRequest,
	sources []renameSourceFile, sourceIsDirectory bool,
) (renameMove, error) {
	var overwritten []string
	if sourceIsDirectory {
		if req.Overwrite {
			return renameMove{}, NewError(ErrCodeInvalidArgument, "overwrite applies only to a file; a directory moves only to a vacant path", false)
		}
		if !req.crossProject() && strings.HasPrefix(req.ToPath, req.FromPath+"/") {
			return renameMove{}, NewError(ErrCodeInvalidPath, "destination cannot be within source subtree", false)
		}
		if err := s.checkVacantDestinationTx(ctx, tx, apiKeyHash, req.TargetProject, req.ToPath); err != nil {
			return renameMove{}, err
		}
	} else {
		var err error
		if overwritten, err = s.checkFileDestinationTx(ctx, tx, apiKeyHash, req.TargetProject, req.ToPath, req.Overwrite); err != nil {
			return renameMove{}, err
		}
	}
	mappings, err := buildRenameMappings(sources, req.FromPath, req.ToPath, sourceIsDirectory)
	if err != nil {
		return renameMove{}, err
	}
	return renameMove{req: req, sources: sources, mappings: mappings, overwritten: overwritten}, nil
}

// applyRenameMoveTx applies one planned move: it releases the overwritten
// destination file, enforces the target quota of a cross-project move and
// remaps the rows, their history and their index jobs. It returns how many
// files moved.
func (s *Service) applyRenameMoveTx(ctx context.Context, tx *sql.Tx, auth AuthContext, move renameMove) (int, error) {
	owner := systemOwnerFromContext(ctx)
	now := s.clock()
	// An overwrite replaces the destination like a write: the replaced bytes
	// stay restorable in the destination path's history.
	if err := s.softDeletePathsTx(ctx, tx, auth, move.req.TargetProject, move.overwritten, now, false); err != nil {
		return 0, err
	}
	if move.req.crossProject() {
		// Overwritten destinations are already released, so the quota sees the
		// target project exactly as it will be after the move commits.
		if err := s.ensureProjectQuota(ctx, tx, auth.APIKeyHash, move.req.TargetProject, renameSourceBytes(move.sources), nil); err != nil {
			return 0, err
		}
	}
	if err := s.applyRenameMappingsTx(ctx, tx, auth, move.req, owner, move.mappings, now); err != nil {
		return 0, err
	}
	return len(move.mappings), nil
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
	below, belowArgs := belowPathSQL("path", fromPath)
	rows, err := tx.QueryContext(ctx,
		rebindSQL(`SELECT id, path, size, skip_rag_index, content_hash FROM mcp_files WHERE apikey_hash = ? AND project = ? AND deleted = FALSE AND system_owner = ? AND `+below+` ORDER BY path ASC`, s.isPostgres),
		append([]any{apiKeyHash, project, owner}, belowArgs...)...,
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

// buildRenameMappings computes the destination path of every selected source
// and validates each one, so a directory move cannot create a path the
// ordinary path rules reject, such as one over the length limit.
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
		if err := ValidatePath(newPath); err != nil {
			return nil, errors.WithStack(err)
		}
		mappings = append(mappings, RenameMapping{ID: source.ID, OldPath: source.Path, NewPath: newPath, SkipRAGIndex: source.SkipRAGIndex, ContentHash: source.ContentHash})
	}
	return mappings, nil
}

// checkVacantDestinationTx is the destination check of a directory move. The
// destination is occupied by a file at toPath, by any file below it (FileIO
// directories are implicit, so every existing directory has one) or by a file
// ancestor, and an occupied destination is rejected: directories are relocated,
// never merged or replaced. It runs under the project locks before any change.
func (s *Service) checkVacantDestinationTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, toPath string) error {
	if err := s.ensureNoParentFile(ctx, tx, apiKeyHash, project, toPath); err != nil {
		return err
	}
	below, belowArgs := belowPathSQL("path", toPath)
	occupied, err := s.liveFileExistsTx(ctx, tx, apiKeyHash, project, "(path = ? OR "+below+")", append([]any{toPath}, belowArgs...))
	if err != nil {
		return err
	}
	if occupied {
		return NewError(ErrCodeAlreadyExists, "destination path already exists; a directory moves only to a vacant path", false)
	}
	return nil
}

// checkFileDestinationTx is the destination check of a single-file rename. The
// destination must not be a directory or have a file ancestor; an existing
// file there is replaced only with overwrite, and is returned for release.
func (s *Service) checkFileDestinationTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, toPath string, overwrite bool) ([]string, error) {
	if err := s.ensureNoParentFile(ctx, tx, apiKeyHash, project, toPath); err != nil {
		return nil, err
	}
	below, belowArgs := belowPathSQL("path", toPath)
	isDirectory, err := s.liveFileExistsTx(ctx, tx, apiKeyHash, project, below, belowArgs)
	if err != nil {
		return nil, err
	}
	if isDirectory {
		return nil, NewError(ErrCodeAlreadyExists, "destination path already exists", false)
	}
	exists, err := s.liveFileExistsTx(ctx, tx, apiKeyHash, project, "path = ?", []any{toPath})
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	if !overwrite {
		return nil, NewError(ErrCodeAlreadyExists, "destination path already exists", false)
	}
	return []string{toPath}, nil
}

// liveFileExistsTx reports whether project has a live file whose row matches
// predicate, a SQL condition with placeholders bound to args.
func (s *Service) liveFileExistsTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, predicate string, args []any) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx,
		rebindSQL(`SELECT 1 FROM mcp_files WHERE apikey_hash = ? AND project = ? AND deleted = FALSE AND system_owner = ? AND `+predicate+` LIMIT 1`, s.isPostgres),
		append([]any{apiKeyHash, project, systemOwnerFromContext(ctx)}, args...)...,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, errors.Wrap(err, "query rename destination")
	}
	return true, nil
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
			credentialID, err := s.storeCredentialEnvelopeTx(ctx, tx, auth, req.TargetProject, mapping.NewPath)
			if err != nil {
				return err
			}
			if err := s.insertIndexJobTx(ctx, tx, FileIndexJob{
				CredentialID: credentialID,
				APIKeyHash:   auth.APIKeyHash, Project: req.TargetProject, FilePath: mapping.NewPath,
				Operation: indexOperationUpsert, FileUpdatedAt: &now, ContentHash: mapping.ContentHash, Status: indexStatusPending, RetryCount: 0,
				AvailableAt: now, CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				return errors.Wrap(err, "enqueue rename upsert job")
			}
		}
	}

	return nil
}
