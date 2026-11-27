package files

import (
	"context"
	"database/sql"
	"maps"
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
// It runs once for every physical project pair the mutation changed.
type renameStateTx func(ctx context.Context, tx *sql.Tx, change RenameStateChange) error

// Rename renames or moves a file path or directory subtree within one project.
func (s *Service) Rename(ctx context.Context, auth AuthContext, project, fromPath, toPath string, overwrite bool) (RenameResult, error) {
	return s.rename(ctx, auth, renameRequest{project: project, fromPath: fromPath, targetProject: project, toPath: toPath, overwrite: overwrite}, nil)
}

// RenameAcrossProjects moves a file or directory subtree from project into
// targetProject of the same caller in one transaction. File identity, version
// history and summary metadata travel with the rows. The source is the file the
// project shows at fromPath, which may be stored in DefaultProject; moving it
// removes it from there. The destination is the physical targetProject: its
// collision, overwrite and precondition rules see only targetProject's own
// files, and the moved bytes must fit its quota. An empty targetProject keeps
// the move inside project.
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
	// Only the source of a cross-project move resolves through the default
	// layer; its destination is the explicit physical target project.
	touched := []string{req.fromPath}
	if !req.crossProject() {
		touched = append(touched, req.toPath)
	}
	movedCount := 0
	err := s.runUserMutation(ctx, auth.APIKeyHash, req.project, []string{req.project, req.targetProject}, touched, func(tx *sql.Tx, overlay bool) error {
		if err := s.checkRenamePreconditionsTx(ctx, tx, auth, req, conditions, overlay); err != nil {
			return err
		}
		// A conditional no-op must still validate its source, but must not bump revision.
		if samePath {
			return nil
		}
		changes, err := s.applyRenameTx(ctx, tx, auth, req, overlay)
		if err != nil {
			return err
		}
		for _, change := range changes {
			movedCount += len(change.Mappings)
			if mutateState != nil {
				if err := mutateState(ctx, tx, change); err != nil {
					return err
				}
			}
		}
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

// applyRenameTx resolves the sources and moves them, returning one state change
// per physical project pair. Without overlay this is a single physical move.
func (s *Service) applyRenameTx(ctx context.Context, tx *sql.Tx, auth AuthContext, req renameRequest, overlay bool) ([]RenameStateChange, error) {
	sources, sourceIsDirectory, err := s.resolveRenameSources(ctx, tx, auth.APIKeyHash, req.project, req.fromPath, overlay)
	if err != nil {
		return nil, err
	}
	if !overlay {
		change, err := s.moveFilesTx(ctx, tx, auth, req, sources, sourceIsDirectory)
		if err != nil {
			return nil, err
		}
		return []RenameStateChange{change}, nil
	}
	var changes []RenameStateChange
	if !req.crossProject() {
		displaced, err := s.checkOverlayDestinationTx(ctx, tx, auth.APIKeyHash, req, sources, sourceIsDirectory)
		if err != nil {
			return nil, err
		}
		if displaced != "" {
			if err := s.softDeletePathsTx(ctx, tx, auth, req.project, []string{displaced}, s.clock(), systemOwnerFromContext(ctx) == ""); err != nil {
				return nil, err
			}
			changes = append(changes, RenameStateChange{Project: req.project, TargetProject: req.project, OverwrittenPaths: []string{displaced}})
		}
	}
	// Every source moves from its own physical project with the ordinary move
	// rules: within that project for a rename, into the target for a move.
	byProject := make(map[string][]renameSourceFile)
	for _, source := range sources {
		byProject[source.Project] = append(byProject[source.Project], source)
	}
	for _, layer := range sortedProjects(byProject) {
		move := req
		move.project = layer
		if !req.crossProject() {
			move.targetProject = layer
		}
		if !move.crossProject() && move.fromPath == move.toPath {
			// A default moved to the same path of DefaultProject is already there.
			continue
		}
		change, err := s.moveFilesTx(ctx, tx, auth, move, byProject[layer], sourceIsDirectory)
		if err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	return changes, nil
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

// moveFilesTx validates the destination in the physical target project and
// moves the given sources of one physical project there.
func (s *Service) moveFilesTx(ctx context.Context, tx *sql.Tx, auth AuthContext, req renameRequest, sources []renameSourceFile, sourceIsDirectory bool) (RenameStateChange, error) {
	owner := systemOwnerFromContext(ctx)
	if !req.crossProject() && sourceIsDirectory && strings.HasPrefix(req.toPath, req.fromPath+"/") {
		return RenameStateChange{}, NewError(ErrCodeInvalidPath, "destination cannot be within source subtree", false)
	}
	if err := s.ensureNoParentFile(ctx, tx, auth.APIKeyHash, req.targetProject, req.toPath); err != nil {
		return RenameStateChange{}, err
	}
	mappings, err := buildRenameMappings(sources, req.fromPath, req.toPath, sourceIsDirectory)
	if err != nil {
		return RenameStateChange{}, err
	}
	now := s.clock()
	overwritePaths, err := s.validateRenameDestinations(ctx, tx, auth.APIKeyHash, req.targetProject, mappings, req.toPath, sourceIsDirectory, req.overwrite)
	if err != nil {
		return RenameStateChange{}, err
	}
	// A replaced destination keeps a history snapshot. Its index rows are
	// replaced by the moved file's upsert, or cleared for an unindexed file.
	if len(overwritePaths) > 0 {
		if err := s.softDeletePathsTx(ctx, tx, auth, req.targetProject, overwritePaths, now, false); err != nil {
			return RenameStateChange{}, err
		}
	}
	if req.crossProject() {
		// Overwritten destinations are already released, so the quota sees the
		// target project exactly as it will be after the move commits.
		if err := s.ensureProjectQuota(ctx, tx, auth.APIKeyHash, req.targetProject, renameSourceBytes(sources), nil); err != nil {
			return RenameStateChange{}, err
		}
	}
	if err := s.applyRenameMappingsTx(ctx, tx, auth, req, owner, mappings, now); err != nil {
		return RenameStateChange{}, err
	}
	return RenameStateChange{Project: req.project, TargetProject: req.targetProject, Mappings: mappings, OverwrittenPaths: overwritePaths}, nil
}

// checkOverlayDestinationTx judges a same-project rename's destination on the
// merged view, the files the caller sees; moveFilesTx then applies the physical
// rules of each source's own project. It returns the project file an
// overwriting rename of a default must delete because it would hide the moved
// default. A default the project hides is never replaced: the caller has not
// seen it and could not condition on its version.
func (s *Service) checkOverlayDestinationTx(ctx context.Context, tx *sql.Tx, apiKeyHash string, req renameRequest, sources []renameSourceFile, sourceIsDirectory bool) (string, error) {
	if sourceIsDirectory && strings.HasPrefix(req.toPath, req.fromPath+"/") {
		return "", NewError(ErrCodeInvalidPath, "destination cannot be within source subtree", false)
	}
	blocked, err := s.visibleFileAtAncestor(ctx, tx, apiKeyHash, req.project, req.toPath, true)
	if err != nil {
		return "", err
	}
	if blocked {
		return "", NewError(ErrCodeNotDirectory, "parent path is a file", false)
	}
	destination, err := s.resolveActiveFile(ctx, tx, apiKeyHash, req.project, req.toPath, true)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", errors.Wrap(err, "query rename destination")
	}
	below, err := s.visibleDescendants(ctx, tx, apiKeyHash, req.project, req.toPath, true)
	if err != nil {
		return "", err
	}
	if sourceIsDirectory {
		return "", checkOverlayDirectoryDestination(req, sources, destination, below)
	}
	if len(below) > 0 {
		return "", NewError(ErrCodeAlreadyExists, "destination path already exists", false)
	}
	switch {
	case destination == nil:
		return "", nil
	case !req.overwrite:
		return "", NewError(ErrCodeAlreadyExists, "destination path already exists", false)
	case destination.Project == sources[0].Project || destination.Project == DefaultProject:
		// The physical overwrite replaces a destination of the source's own
		// project; a project file moved onto a default shadows it.
		return "", nil
	}
	if _, err := s.findActiveFileQ(ctx, tx, apiKeyHash, DefaultProject, req.toPath); err == nil {
		return "", NewError(ErrCodeAlreadyExists, "destination is also occupied by a default this project hides", false)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", errors.Wrap(err, "query hidden default destination")
	}
	return destination.Path, nil
}

// checkOverlayDirectoryDestination rejects a directory rename whose moved files
// would conflict by name with a visible file that is not itself moved.
func checkOverlayDirectoryDestination(req renameRequest, sources []renameSourceFile, destination *File, below []layerFile) error {
	if destination != nil {
		return NewError(ErrCodeAlreadyExists, "destination path already exists", false)
	}
	moved := make(map[uint64]struct{}, len(sources))
	for _, source := range sources {
		moved[source.ID] = struct{}{}
	}
	occupied := make([]string, 0, len(below))
	for _, file := range below {
		if _, isSource := moved[file.ID]; !isSource {
			occupied = append(occupied, file.Path)
		}
	}
	shadow := newUpperShadow(occupied)
	for _, source := range sources {
		if shadow.hides(req.toPath + strings.TrimPrefix(source.Path, req.fromPath)) {
			return NewError(ErrCodeAlreadyExists, "destination path collision", false)
		}
	}
	return nil
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
func (s *Service) checkRenamePreconditionsTx(ctx context.Context, tx *sql.Tx, auth AuthContext, req renameRequest, p FilePreconditions, overlay bool) error {
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
	source := func(path, expected string, createOnly bool) error {
		return s.checkVersionTx(ctx, tx, auth, req.project, path, expected, createOnly, overlay)
	}
	destination := source
	if req.crossProject() {
		destination = func(path, expected string, createOnly bool) error {
			return s.checkPathVersionTx(ctx, tx, auth, req.targetProject, path, expected, createOnly)
		}
	}
	if err := source(req.fromPath, p.ExpectedVersion, false); err != nil {
		return err
	}
	if !req.crossProject() && req.fromPath == req.toPath {
		return destination(req.toPath, p.ExpectedDestinationVersion, p.DestinationMustNotExist)
	}
	if req.overwrite && p.ExpectedDestinationVersion == "" && !p.DestinationMustNotExist {
		return NewError(ErrCodeInvalidArgument, "conditional overwrite rename requires a destination version or destination_must_not_exist", false)
	}
	if !req.overwrite && p.ExpectedDestinationVersion != "" {
		return NewError(ErrCodeInvalidArgument, "expected_destination_version requires overwrite=true", false)
	}
	return destination(req.toPath, p.ExpectedDestinationVersion, p.DestinationMustNotExist || !req.overwrite)
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

// buildRenameMappings computes destination paths for the selected rename source files.
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
		below, belowArgs := belowPathSQL("path", toPath)
		var descendantCount int64
		if err := tx.QueryRowContext(ctx,
			rebindSQL(`SELECT COUNT(1) FROM mcp_files WHERE apikey_hash = ? AND project = ? AND deleted = FALSE AND system_owner = ? AND `+below, s.isPostgres),
			append([]any{apiKeyHash, project, owner}, belowArgs...)...,
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

// applyRenameMappingsTx moves file/version rows into the destination project
// and path, and schedules their index updates atomically. Index rows are keyed by
// project, so a cross-project move deletes the old index entry in the source
// project and rebuilds the new one in the target project.
func (s *Service) applyRenameMappingsTx(ctx context.Context, tx *sql.Tx, auth AuthContext,
	req renameRequest, owner string, mappings []RenameMapping, now time.Time,
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
			if mapping.SkipRAGIndex {
				// This destination will not get an upsert. Clear old RAG rows
				// atomically, also covering a path reused at the same timestamp.
				if err := s.deleteIndexRowsTx(ctx, tx, auth.APIKeyHash, req.targetProject, mapping.NewPath); err != nil {
					return err
				}
				continue
			}
			if err := s.insertIndexJobTx(ctx, tx, FileIndexJob{
				APIKeyHash: auth.APIKeyHash, Project: req.targetProject, FilePath: mapping.NewPath,
				Operation: indexOperationUpsert, FileUpdatedAt: &now, ContentHash: mapping.ContentHash, Status: indexStatusPending, RetryCount: 0,
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

// mutateMoveSystemStateTx applies one physical change to the system namespaces
// plan maps its projects to. It reads only the selected rows, once, and keeps
// the snapshot to diff mutations without reloading state under the locks.
func (s *Service) mutateMoveSystemStateTx(ctx context.Context, tx *sql.Tx, systemOwner string, change RenameStateChange, plan SystemStateMovePlan) error {
	systemProject, targetSystemProject := plan.SystemProject(change.Project), plan.SystemProject(change.TargetProject)
	if err := ValidateProject(systemProject); err != nil {
		return errors.Wrap(err, "validate source system project")
	}
	if err := ValidateProject(targetSystemProject); err != nil {
		return errors.Wrap(err, "validate target system project")
	}
	if (change.Project == change.TargetProject) != (systemProject == targetSystemProject) {
		return errors.New("project and system namespace move scopes must agree")
	}
	stateCtx := contextWithSystemOwner(ctx, systemOwner)
	sourceSnapshot := make(map[string][]byte)
	if err := s.readSelectedSystemStateTx(stateCtx, tx, systemOwner, systemProject, plan.CatalogPaths, sourceSnapshot); err != nil {
		return err
	}
	var targetSnapshot map[string][]byte
	if systemProject != targetSystemProject {
		targetSnapshot = make(map[string][]byte)
		if err := s.readSelectedSystemStateTx(stateCtx, tx, systemOwner, targetSystemProject, plan.CatalogPaths, targetSnapshot); err != nil {
			return err
		}
	}
	sourcePaths, targetPaths, err := plan.AffectedPaths(change, sourceSnapshot, targetSnapshot)
	if err != nil {
		return errors.Wrap(err, "select rename system state")
	}
	if err := s.readSelectedSystemStateTx(stateCtx, tx, systemOwner, systemProject, sourcePaths, sourceSnapshot); err != nil {
		return err
	}
	if targetSnapshot != nil {
		if err := s.readSelectedSystemStateTx(stateCtx, tx, systemOwner, targetSystemProject, targetPaths, targetSnapshot); err != nil {
			return err
		}
	}
	source, target := maps.Clone(sourceSnapshot), maps.Clone(targetSnapshot)
	if err := plan.Mutate(change, source, target); err != nil {
		return errors.Wrap(err, "mutate rename system state")
	}
	if err := s.persistSystemStateDiffTx(stateCtx, tx, systemOwner, systemProject, sourceSnapshot, source); err != nil {
		return err
	}
	if target != nil {
		return s.persistSystemStateDiffTx(stateCtx, tx, systemOwner, targetSystemProject, targetSnapshot, target)
	}
	return nil
}
