package files

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// renameSourceFile represents an active file selected as a rename source,
// tagged with the physical project that stores it.
type renameSourceFile struct {
	ID           uint64
	Project      string
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
// re-indexes the moved paths after commit. The source is the file, or the
// files below a directory, that the project shows, which may be stored in
// DefaultProject; see applyRenameTx.
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
	// Only the source of a cross-project move resolves through the default
	// layer; its destination is the explicit physical target project.
	touched := []string{req.FromPath}
	if !req.crossProject() {
		touched = append(touched, req.ToPath)
	}
	movedCount := 0
	err := s.runUserMutation(ctx, auth.APIKeyHash, req.Project, []string{req.Project, req.TargetProject}, touched, func(tx *sql.Tx, overlay bool) error {
		if err := s.checkRenamePreconditionsTx(ctx, tx, auth, req, conditions, overlay); err != nil {
			return err
		}
		// A conditional no-op must still validate its source, but must not bump revision.
		if samePath {
			return nil
		}
		var err error
		movedCount, err = s.applyRenameTx(ctx, tx, auth, req, overlay)
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

// applyRenameTx resolves the sources and moves them, returning how many files
// moved. Each source moves from the physical project that stores it, with the
// ordinary move rules of planRenameMoveTx: within that project for a rename
// (so renaming an inherited default renames it in DefaultProject), into the
// target project for a cross-project move. A same-project rename of an
// overlaid project is also judged on the merged view (checkOverlayDestinationTx).
// Every move is planned before the first change, so no destination check sees
// files an earlier move of the same request placed there, and a rejected
// rename leaves rows, history and the index outbox untouched.
func (s *Service) applyRenameTx(ctx context.Context, tx *sql.Tx, auth AuthContext, req RenameRequest, overlay bool) (int, error) {
	sources, sourceIsDirectory, err := s.resolveRenameSources(ctx, tx, auth.APIKeyHash, req.Project, req.FromPath, overlay)
	if err != nil {
		return 0, err
	}
	displaced := ""
	if overlay && !req.crossProject() {
		if displaced, err = s.checkOverlayDestinationTx(ctx, tx, auth.APIKeyHash, req, sources, sourceIsDirectory); err != nil {
			return 0, err
		}
	}
	byProject := make(map[string][]renameSourceFile)
	for _, source := range sources {
		byProject[source.Project] = append(byProject[source.Project], source)
	}
	moves := make([]renameMove, 0, len(byProject))
	for _, layer := range sortedProjects(byProject) {
		move := req
		move.Project = layer
		if !req.crossProject() {
			move.TargetProject = layer
		}
		if !move.crossProject() && move.FromPath == move.ToPath {
			// A default moved to its own path of DefaultProject is already there.
			continue
		}
		planned, err := s.planRenameMoveTx(ctx, tx, auth.APIKeyHash, move, byProject[layer], sourceIsDirectory)
		if err != nil {
			return 0, err
		}
		moves = append(moves, planned)
	}
	if displaced != "" {
		if err := s.softDeletePathsTx(ctx, tx, auth, req.Project, []string{displaced}, s.clock(), systemOwnerFromContext(ctx) == ""); err != nil {
			return 0, err
		}
	}
	movedCount := 0
	for _, move := range moves {
		n, err := s.applyRenameMoveTx(ctx, tx, auth, move)
		if err != nil {
			return 0, err
		}
		movedCount += n
	}
	return movedCount, nil
}

// sortedProjects returns the keys of grouped with the addressed project before DefaultProject.
func sortedProjects(grouped map[string][]renameSourceFile) []string {
	projects := make([]string, 0, len(grouped))
	for project := range grouped {
		projects = append(projects, project)
	}
	sort.Slice(projects, func(i, j int) bool {
		if (projects[i] == DefaultProject) != (projects[j] == DefaultProject) {
			return projects[j] == DefaultProject
		}
		return projects[i] < projects[j]
	})
	return projects
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

// checkOverlayDestinationTx judges a same-project rename's destination on the
// merged view, the files the caller sees; moveFilesTx then applies the physical
// rules of each source's own project. It returns the project file an
// overwriting rename of a default must delete because it would hide the moved
// default. A default the project hides is never replaced: the caller has not
// seen it and could not condition on its version.
func (s *Service) checkOverlayDestinationTx(ctx context.Context, tx *sql.Tx, apiKeyHash string, req RenameRequest, sources []renameSourceFile, sourceIsDirectory bool) (string, error) {
	if sourceIsDirectory && req.Overwrite {
		return "", NewError(ErrCodeInvalidArgument, "overwrite applies only to a file; a directory moves only to a vacant path", false)
	}
	if sourceIsDirectory && strings.HasPrefix(req.ToPath, req.FromPath+"/") {
		return "", NewError(ErrCodeInvalidPath, "destination cannot be within source subtree", false)
	}
	blocked, err := s.visibleFileAtAncestor(ctx, tx, apiKeyHash, req.Project, req.ToPath, true)
	if err != nil {
		return "", err
	}
	if blocked {
		return "", NewError(ErrCodeNotDirectory, "parent path is a file", false)
	}
	destination, err := s.resolveActiveFile(ctx, tx, apiKeyHash, req.Project, req.ToPath, true)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", errors.Wrap(err, "query rename destination")
	}
	below, err := s.visibleDescendants(ctx, tx, apiKeyHash, req.Project, req.ToPath, true)
	if err != nil {
		return "", err
	}
	if sourceIsDirectory {
		// The merged view has the same relocation rule as a physical project:
		// a visible file at or below the destination occupies it.
		if destination != nil || len(below) > 0 {
			return "", NewError(ErrCodeAlreadyExists, "destination path already exists; a directory moves only to a vacant path", false)
		}
		return "", nil
	}
	if len(below) > 0 {
		return "", NewError(ErrCodeAlreadyExists, "destination path already exists", false)
	}
	switch {
	case destination == nil:
		return "", nil
	case !req.Overwrite:
		return "", NewError(ErrCodeAlreadyExists, "destination path already exists", false)
	case destination.Project == sources[0].Project || destination.Project == DefaultProject:
		// The physical overwrite replaces a destination of the source's own
		// project; a project file moved onto a default shadows it.
		return "", nil
	}
	if _, err := s.findActiveFileQ(ctx, tx, apiKeyHash, DefaultProject, req.ToPath); err == nil {
		return "", NewError(ErrCodeAlreadyExists, "destination is also occupied by a default this project hides", false)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", errors.Wrap(err, "query hidden default destination")
	}
	return destination.Path, nil
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
// The source and a same-project destination are checked against the files the
// project shows; a cross-project destination against the physical target row.
func (s *Service) checkRenamePreconditionsTx(ctx context.Context, tx *sql.Tx, auth AuthContext, req RenameRequest, p FilePreconditions, overlay bool) error {
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
	source := func(path, expected string, createOnly bool) error {
		return s.checkVersionTx(ctx, tx, auth, req.Project, path, expected, createOnly, overlay)
	}
	destination := source
	if req.crossProject() {
		destination = func(path, expected string, createOnly bool) error {
			return s.checkPathVersionTx(ctx, tx, auth, req.TargetProject, path, expected, createOnly)
		}
	}
	if err := source(req.FromPath, p.ExpectedVersion, false); err != nil {
		return err
	}
	if !req.crossProject() && req.FromPath == req.ToPath {
		return destination(req.ToPath, p.ExpectedDestinationVersion, p.DestinationMustNotExist)
	}
	if req.Overwrite && p.ExpectedDestinationVersion == "" && !p.DestinationMustNotExist {
		return NewError(ErrCodeInvalidArgument, "conditional overwrite rename requires a destination version or destination_must_not_exist", false)
	}
	if !req.Overwrite && p.ExpectedDestinationVersion != "" {
		return NewError(ErrCodeInvalidArgument, "expected_destination_version requires overwrite=true", false)
	}
	return destination(req.ToPath, p.ExpectedDestinationVersion, p.DestinationMustNotExist || !req.Overwrite)
}

// resolveRenameSources resolves the file at fromPath, or else the files below
// it, that the project shows; without overlay these are physical rows.
func (s *Service) resolveRenameSources(ctx context.Context, tx *sql.Tx, apiKeyHash, project, fromPath string, overlay bool) ([]renameSourceFile, bool, error) {
	file, err := s.resolveActiveFile(ctx, tx, apiKeyHash, project, fromPath, overlay)
	if err == nil {
		return []renameSourceFile{{
			ID: file.ID, Project: file.Project, Path: file.Path, Size: file.Size, SkipRAGIndex: file.SkipRAGIndex, ContentHash: file.ContentHash,
		}}, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, errors.Wrap(err, "query rename source file")
	}
	visible, err := s.visibleDescendants(ctx, tx, apiKeyHash, project, fromPath, overlay)
	if err != nil {
		return nil, false, err
	}
	if len(visible) == 0 {
		return nil, false, NewError(ErrCodeNotFound, "source path not found", false)
	}
	sort.Slice(visible, func(i, j int) bool { return visible[i].Path < visible[j].Path })
	sources := make([]renameSourceFile, 0, len(visible))
	for _, row := range visible {
		sources = append(sources, renameSourceFile{
			ID: row.ID, Project: row.Project, Path: row.Path, Size: row.Size, SkipRAGIndex: row.SkipRAGIndex, ContentHash: row.ContentHash,
		})
	}
	return sources, true, nil
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
