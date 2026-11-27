package files

import (
	"context"
	"database/sql"
	"slices"
	"sort"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// DefaultProject is the reserved, per-API-key base layer of every other project.
//
// The layer is a filesystem view, not a copy. For every user project P of a
// tenant, stat, read and list merge the two physical projects: a directory is
// the recursive union of both layers' children, and at a conflicting name P's
// entry wins. A P file therefore hides the DefaultProject file at the same path
// and the whole DefaultProject subtree below it; a P directory hides a
// DefaultProject file of the same name. The same view resolves the existing
// file a write, delete, restore or same-project rename addresses, so changing
// an inherited file changes it in DefaultProject for every project, while a new
// name is created in P. History is that resolved file's own physical history,
// never a union of the layers.
//
// Retrieval is physical: file_search reads P and DefaultProject as two physical
// projects, including documents the view hides, and every hit names the
// project that stores it. DefaultProject itself, the search wildcard and system
// namespaces never overlay, and tenants (API-key hashes) remain isolated.
// See docs/proposals/fileio_default_project.md.
const DefaultProject = "@DEFAULT"

// IsDefaultProject reports whether project names the reserved base layer.
func IsDefaultProject(project string) bool {
	return project == DefaultProject
}

// physicalProjectsKey marks a context whose FileIO calls address physical
// projects only.
type physicalProjectsKey struct{}

// WithPhysicalProjects returns ctx for automated owners of project-local state,
// such as the memory engine and shadow replay. FileIO calls made with it address
// only the named physical project: they never show, change or delete a
// DefaultProject file through another project.
func WithPhysicalProjects(ctx context.Context) context.Context {
	return context.WithValue(ctx, physicalProjectsKey{}, true)
}

// overlayApplies reports whether project reads and resolves through the
// DefaultProject layer.
func overlayApplies(ctx context.Context, project string) bool {
	if project == DefaultProject || project == ProjectWildcard || systemOwnerFromContext(ctx) != "" {
		return false
	}
	physical, _ := ctx.Value(physicalProjectsKey{}).(bool)
	return !physical
}

// sqlQueryer is the read surface shared by *sql.DB and *sql.Tx, so the same
// resolution runs inside a mutation transaction, a read snapshot or the pool.
type sqlQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// withReadSnapshot runs a multi-statement read of an overlaid project on one
// consistent snapshot, so a stat or list never combines two generations of the
// layers. It takes no lock. Physical reads keep using the pool.
func (s *Service) withReadSnapshot(ctx context.Context, overlay bool, fn func(q sqlQueryer) error) error {
	if !overlay {
		return fn(s.db)
	}
	opts := &sql.TxOptions{ReadOnly: true}
	if s.isPostgres {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		return errors.Wrap(err, "begin read snapshot")
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return errors.Wrap(tx.Commit(), "end read snapshot")
}

// layerFile is the metadata of one live file row, tagged with its physical project.
type layerFile struct {
	ID           uint64
	Project      string
	Path         string
	Size         int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
	SkipRAGIndex bool
	ContentHash  string
}

// layerFileColumns are the columns queryLayerFiles scans.
const layerFileColumns = `id, project, path, size, created_at, updated_at, skip_rag_index, content_hash`

// upperShadow answers whether the files of an upper layer hide a lower-layer path.
type upperShadow struct {
	files map[string]struct{}
	dirs  map[string]struct{}
}

// newUpperShadow indexes upper file paths and every implicit directory above them.
func newUpperShadow(paths []string) upperShadow {
	shadow := upperShadow{files: make(map[string]struct{}, len(paths)), dirs: make(map[string]struct{})}
	for _, path := range paths {
		shadow.files[path] = struct{}{}
		for _, parent := range parentPaths(path) {
			shadow.dirs[parent] = struct{}{}
		}
	}
	return shadow
}

// hides reports whether a lower file at path conflicts with the upper layer by
// name: the upper layer has a file at the path or at an ancestor, or a directory
// (descendants) at the path. Sibling names never conflict, so directories merge.
func (u upperShadow) hides(path string) bool {
	if _, ok := u.files[path]; ok {
		return true
	}
	if _, ok := u.dirs[path]; ok {
		return true
	}
	for _, parent := range parentPaths(path) {
		if _, ok := u.files[parent]; ok {
			return true
		}
	}
	return false
}

// fileMetadataColumns are the columns of a live file row except its content.
const fileMetadataColumns = `f.id, f.apikey_hash, f.project, f.path, f.size, f.created_at, f.updated_at, f.deleted, f.deleted_at,
	f.skip_rag_index, f.content_hash, f.summary_content_hash, f.summary_status, f.summary_generation_key, f.incarnation_id || ':' || CAST(f.revision AS TEXT)`

// scanFileRow scans fileMetadataColumns, preceded by content when withContent is set.
func scanFileRow(row *sql.Row, withContent bool) (*File, error) {
	var file File
	dest := []any{
		&file.ID, &file.APIKeyHash, &file.Project, &file.Path, &file.Size, &file.CreatedAt, &file.UpdatedAt, &file.Deleted, &file.DeletedAt,
		&file.SkipRAGIndex, &file.ContentHash, &file.SummaryContentHash, &file.SummaryStatus, &file.SummaryGenerationKey, &file.Version,
	}
	if withContent {
		dest = append([]any{&file.Content}, dest...)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	return &file, nil
}

// fileColumns returns the select list scanFileRow expects.
func fileColumns(withContent bool) string {
	if withContent {
		return "f.content, " + fileMetadataColumns
	}
	return fileMetadataColumns
}

// findActiveFileQ loads one complete live file row of a single physical project.
func (s *Service) findActiveFileQ(ctx context.Context, q sqlQueryer, apiKeyHash, project, path string) (*File, error) {
	return s.findFileRow(ctx, q, apiKeyHash, project, path, true)
}

// findFileRow loads one live file row of a single physical project.
func (s *Service) findFileRow(ctx context.Context, q sqlQueryer, apiKeyHash, project, path string, withContent bool) (*File, error) {
	return scanFileRow(q.QueryRowContext(ctx,
		rebindSQL(`SELECT `+fileColumns(withContent)+` FROM mcp_files f
			WHERE f.apikey_hash = ? AND f.project = ? AND f.path = ? AND f.deleted = FALSE AND f.system_owner = ?
			LIMIT 1`, s.isPostgres),
		apiKeyHash, project, path, systemOwnerFromContext(ctx),
	), withContent)
}

// resolveActiveFile returns the metadata of the live file a path of project
// resolves to; with overlay it is the file the merged view shows. File.Project
// names the physical project that stores it. sql.ErrNoRows means no file is
// visible there.
func (s *Service) resolveActiveFile(ctx context.Context, q sqlQueryer, apiKeyHash, project, path string, overlay bool) (*File, error) {
	return s.resolveFile(ctx, q, apiKeyHash, project, path, overlay, false)
}

// resolveFile is resolveActiveFile, optionally loading the content too.
func (s *Service) resolveFile(ctx context.Context, q sqlQueryer, apiKeyHash, project, path string, overlay, withContent bool) (*File, error) {
	if !overlay || path == "" {
		return s.findFileRow(ctx, q, apiKeyHash, project, path, withContent)
	}
	// One statement, one snapshot: the project's own row wins, and the default
	// row qualifies only while the project has no file at the path, at an
	// ancestor, or below it.
	conflicts := append([]string{path}, parentPaths(path)...)
	below, belowArgs := belowPathSQL("o.path", path)
	args := make([]any, 0, 6+len(conflicts)+len(belowArgs))
	args = append(args, apiKeyHash, path, systemOwnerFromContext(ctx), project, DefaultProject, project)
	for _, conflict := range conflicts {
		args = append(args, conflict)
	}
	args = append(args, belowArgs...)
	return scanFileRow(q.QueryRowContext(ctx,
		rebindSQL(`SELECT `+fileColumns(withContent)+` FROM mcp_files f
			WHERE f.apikey_hash = ? AND f.path = ? AND f.deleted = FALSE AND f.system_owner = ?
			AND (f.project = ? OR (f.project = ? AND NOT EXISTS (SELECT 1 FROM mcp_files o
				WHERE o.apikey_hash = f.apikey_hash AND o.project = ? AND o.deleted = FALSE AND o.system_owner = f.system_owner
				AND (o.path IN (`+placeholders(len(conflicts))+`) OR `+below+`))))
			LIMIT 1`, s.isPostgres),
		args...,
	), withContent)
}

// layerDescendants returns the live file rows strictly below path in one physical project.
func (s *Service) layerDescendants(ctx context.Context, q sqlQueryer, apiKeyHash, project, path string) ([]layerFile, error) {
	below, belowArgs := belowPathSQL("path", path)
	args := append([]any{apiKeyHash, project, systemOwnerFromContext(ctx)}, belowArgs...)
	return s.queryLayerFiles(ctx, q, `SELECT `+layerFileColumns+` FROM mcp_files
		WHERE apikey_hash = ? AND project = ? AND deleted = FALSE AND system_owner = ? AND `+below, args)
}

// visibleDescendants returns the files below path that project shows, each
// tagged with its physical project. Without overlay these are the project's own
// descendants. With overlay a single statement reads both layers below path and
// the project's files at path and its ancestors, and the name-conflict rule of
// upperShadow merges them.
func (s *Service) visibleDescendants(ctx context.Context, q sqlQueryer, apiKeyHash, project, path string, overlay bool) ([]layerFile, error) {
	if !overlay {
		return s.layerDescendants(ctx, q, apiKeyHash, project, path)
	}
	below, belowArgs := belowPathSQL("path", path)
	query := `SELECT ` + layerFileColumns + ` FROM mcp_files
		WHERE apikey_hash = ? AND deleted = FALSE AND system_owner = ? AND project IN (?, ?) AND (` + below
	args := append([]any{apiKeyHash, systemOwnerFromContext(ctx), project, DefaultProject}, belowArgs...)
	if path != "" {
		above := append([]string{path}, parentPaths(path)...)
		query += ` OR (project = ? AND path IN (` + placeholders(len(above)) + `))`
		args = append(args, project)
		for _, candidate := range above {
			args = append(args, candidate)
		}
	}
	rows, err := s.queryLayerFiles(ctx, q, query+`)`, args)
	if err != nil {
		return nil, err
	}
	var upper, lower []layerFile
	subtreeHidden := false
	for _, row := range rows {
		switch {
		case row.Project == project && (row.Path == path || !strings.HasPrefix(row.Path, path+"/")):
			// A project file at path or above it hides the whole default subtree.
			subtreeHidden = true
		case row.Project == project:
			upper = append(upper, row)
		default:
			lower = append(lower, row)
		}
	}
	if subtreeHidden || len(lower) == 0 {
		return upper, nil
	}
	upperPaths := make([]string, 0, len(upper))
	for _, row := range upper {
		upperPaths = append(upperPaths, row.Path)
	}
	shadow := newUpperShadow(upperPaths)
	for _, row := range lower {
		if !shadow.hides(row.Path) {
			upper = append(upper, row)
		}
	}
	return upper, nil
}

// queryLayerFiles runs a layer-file query and scans its rows.
func (s *Service) queryLayerFiles(ctx context.Context, q sqlQueryer, query string, args []any) ([]layerFile, error) {
	rows, err := q.QueryContext(ctx, rebindSQL(query, s.isPostgres), args...)
	if err != nil {
		return nil, errors.Wrap(err, "query layer files")
	}
	defer func() { _ = rows.Close() }()
	var files []layerFile
	for rows.Next() {
		var row layerFile
		if err := rows.Scan(&row.ID, &row.Project, &row.Path, &row.Size, &row.CreatedAt, &row.UpdatedAt, &row.SkipRAGIndex, &row.ContentHash); err != nil {
			return nil, errors.Wrap(err, "scan layer file")
		}
		files = append(files, row)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate layer files")
	}
	return files, nil
}

// layerHasDescendants reports, with a bounded probe, whether one physical
// project has any live file strictly below path.
func (s *Service) layerHasDescendants(ctx context.Context, q sqlQueryer, apiKeyHash, project, path string) (bool, error) {
	below, belowArgs := belowPathSQL("path", path)
	var id uint64
	err := q.QueryRowContext(ctx,
		rebindSQL(`SELECT id FROM mcp_files WHERE apikey_hash = ? AND project = ? AND deleted = FALSE AND system_owner = ? AND `+below+` LIMIT 1`, s.isPostgres),
		append([]any{apiKeyHash, project, systemOwnerFromContext(ctx)}, belowArgs...)...,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, errors.Wrap(err, "probe layer descendants")
	}
	return true, nil
}

// defaultLayerTouches reports whether the tenant's DefaultProject holds a live
// user file at, above, or below any of paths. A mutation of project paths the
// default layer does not touch neither reads nor changes that layer, so it keeps
// its original lock scope and physical code path. If a default file appears
// concurrently, the result equals running the mutation first.
func (s *Service) defaultLayerTouches(ctx context.Context, q sqlQueryer, apiKeyHash string, paths ...string) (bool, error) {
	exact := make([]string, 0, len(paths)*2)
	conditions := make([]string, 0, len(paths)+1)
	args := []any{apiKeyHash, DefaultProject, systemOwnerFromContext(ctx)}
	for _, path := range paths {
		below, belowArgs := belowPathSQL("path", path)
		conditions = append(conditions, below)
		args = append(args, belowArgs...)
		if path != "" {
			exact = append(exact, path)
			exact = append(exact, parentPaths(path)...)
		}
	}
	if len(exact) > 0 {
		conditions = append(conditions, `path IN (`+placeholders(len(exact))+`)`)
		for _, path := range exact {
			args = append(args, path)
		}
	}
	var id uint64
	err := q.QueryRowContext(ctx,
		rebindSQL(`SELECT id FROM mcp_files WHERE apikey_hash = ? AND project = ? AND deleted = FALSE AND system_owner = ?
			AND (`+strings.Join(conditions, " OR ")+`) LIMIT 1`, s.isPostgres),
		args...,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, errors.Wrap(err, "check default layer")
	}
	return true, nil
}

// errOverlayRequired aborts an attempt whose paths the default layer touches,
// so the mutation can restart while also holding DefaultProject's lock.
var errOverlayRequired = errors.New("mutation requires the default layer")

// runUserMutation runs fn in one transaction holding every scope lock. When
// project overlays DefaultProject, the first attempt checks inside that
// transaction whether the default layer touches paths. If it does not, fn runs
// physically and never reads DefaultProject, which keeps the original behavior
// and lock scope. If it does, the attempt is rolled back before fn runs, and fn
// reruns in overlay mode with DefaultProject added to the scopes; every scope is
// always acquired in ProjectLockOrder.
func (s *Service) runUserMutation(ctx context.Context, apiKeyHash, project string, scopes, paths []string,
	fn func(tx *sql.Tx, overlay bool) error,
) error {
	if !overlayApplies(ctx, project) {
		return s.withProjectLocks(ctx, apiKeyHash, scopes, func(tx *sql.Tx) error {
			return fn(tx, false)
		})
	}
	defaultLocked := slices.Contains(scopes, DefaultProject)
	err := s.withProjectLocks(ctx, apiKeyHash, scopes, func(tx *sql.Tx) error {
		touches, err := s.defaultLayerTouches(ctx, tx, apiKeyHash, paths...)
		switch {
		case err != nil:
			return err
		case !touches:
			return fn(tx, false)
		case defaultLocked:
			return fn(tx, true)
		}
		return errOverlayRequired
	})
	if !errors.Is(err, errOverlayRequired) {
		return err
	}
	return s.withProjectLocks(ctx, apiKeyHash, append(slices.Clone(scopes), DefaultProject), func(tx *sql.Tx) error {
		return fn(tx, true)
	})
}

// visibleFileAtAncestor reports whether project shows a file at any ancestor of
// path, which makes path unusable as a file or directory name.
func (s *Service) visibleFileAtAncestor(ctx context.Context, q sqlQueryer, apiKeyHash, project, path string, overlay bool) (bool, error) {
	for _, parent := range parentPaths(path) {
		_, err := s.resolveActiveFile(ctx, q, apiKeyHash, project, parent, overlay)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, errors.Wrap(err, "resolve ancestor file")
		}
	}
	return false, nil
}

// fileNamespaceQ returns the physical project of the file a path of project
// addresses, resolved like a normal read (resolveActiveFile), and that file;
// the file's history belongs to the same physical project. With overlay a
// visible file names its own project: a project file overriding a default, or
// an inherited default. Without a visible file (an absent or deleted path, or a
// directory) and without overlay it is the addressed project, so a write
// creates the name there and a deleted file stays recoverable from its own
// history without falling back to unrelated default history. current is nil
// when no file is visible or overlay is off.
func (s *Service) fileNamespaceQ(ctx context.Context, q sqlQueryer, apiKeyHash, project, path string, overlay bool) (string, *File, error) {
	if !overlay {
		return project, nil, nil
	}
	current, err := s.resolveActiveFile(ctx, q, apiKeyHash, project, path, true)
	switch {
	case err == nil:
		return current.Project, current, nil
	case errors.Is(err, sql.ErrNoRows):
		return project, nil, nil
	}
	return "", nil, errors.Wrap(err, "resolve file namespace")
}

// resolveWriteLayerTx selects the physical project an overlay write changes:
// fileNamespaceQ's project. A new file must also be a legal name in the merged
// view.
func (s *Service) resolveWriteLayerTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, path string) (string, error) {
	layer, current, err := s.fileNamespaceQ(ctx, tx, apiKeyHash, project, path, true)
	if err != nil || current != nil {
		return layer, err
	}
	descendants, err := s.visibleDescendants(ctx, tx, apiKeyHash, project, path, true)
	if err != nil {
		return "", err
	}
	if len(descendants) > 0 {
		return "", NewError(ErrCodeIsDirectory, "path has descendant files", false)
	}
	blocked, err := s.visibleFileAtAncestor(ctx, tx, apiKeyHash, project, path, true)
	if err != nil {
		return "", err
	}
	if blocked {
		return "", NewError(ErrCodeNotDirectory, "parent path is a file", false)
	}
	return project, nil
}

// checkVersionTx enforces a caller condition against the file the path of
// project resolves to: with overlay the file the merged view shows, never a
// hidden layer; otherwise the physical row.
func (s *Service) checkVersionTx(ctx context.Context, tx *sql.Tx, auth AuthContext, project, path, expected string, createOnly, overlay bool) error {
	if !overlay {
		return s.checkPathVersionTx(ctx, tx, auth, project, path, expected, createOnly)
	}
	if expected == "" && !createOnly {
		return nil
	}
	actual := ""
	file, err := s.resolveActiveFile(ctx, tx, auth.APIKeyHash, project, path, true)
	switch {
	case err == nil:
		actual = file.Version
	case !errors.Is(err, sql.ErrNoRows):
		return errors.Wrap(err, "read effective file version")
	}
	return checkFileVersion(actual, expected, createOnly)
}

// groupPathsByProject groups layer files by physical project with paths sorted.
// The addressed project is listed before DefaultProject.
func groupPathsByProject(files []layerFile) ([]string, map[string][]string) {
	grouped := make(map[string][]string)
	for _, file := range files {
		grouped[file.Project] = append(grouped[file.Project], file.Path)
	}
	projects := make([]string, 0, len(grouped))
	for project, paths := range grouped {
		sort.Strings(paths)
		projects = append(projects, project)
	}
	sort.Slice(projects, func(i, j int) bool {
		if (projects[i] == DefaultProject) != (projects[j] == DefaultProject) {
			return projects[j] == DefaultProject
		}
		return projects[i] < projects[j]
	})
	return projects, grouped
}

// placeholders returns n comma-separated "?" placeholders for rebindSQL.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
