package files

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// DefaultProject is the reserved, per-API-key base layer of every other project.
//
// Every user project P of a tenant reads a merged view: P's own files win, and a
// DefaultProject file shows through wherever P has no file at the same path, at
// one of its ancestors, or below it. Writing, deleting, or renaming a path that
// resolves to a DefaultProject file changes that file for every project of the
// tenant. New files are always created in the project that was addressed.
// DefaultProject itself, the file_search wildcard, and system namespaces are
// physical and never overlay. Tenants (API-key hashes) remain fully isolated.
// See docs/proposals/fileio_default_project.md.
const DefaultProject = "@DEFAULT"

// IsDefaultProject reports whether project names the reserved base layer.
func IsDefaultProject(project string) bool {
	return project == DefaultProject
}

// overlayApplies reports whether project reads through the DefaultProject layer.
func overlayApplies(ctx context.Context, project string) bool {
	return project != DefaultProject && project != ProjectWildcard && systemOwnerFromContext(ctx) == ""
}

// sqlQueryer is the read surface shared by *sql.DB and *sql.Tx, so the same
// resolution runs inside a mutation transaction or against the pool.
type sqlQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// layerFile is the metadata of one live file row, tagged with its physical project.
type layerFile struct {
	ID        uint64
	Project   string
	Path      string
	Size      int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

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

// hides reports whether a lower file at path is invisible: the upper layer has a
// file at the path or at an ancestor, or has a directory (descendants) at the path.
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

// findActiveFileQ loads one live file row of a single physical project.
func (s *Service) findActiveFileQ(ctx context.Context, q sqlQueryer, apiKeyHash, project, path string) (*File, error) {
	owner := systemOwnerFromContext(ctx)
	var file File
	err := q.QueryRowContext(ctx,
		rebindSQL(`SELECT id, apikey_hash, project, path, content, size, created_at, updated_at, deleted, deleted_at,
			content_hash, summary_content_hash, summary_status, summary_generation_key, incarnation_id || ':' || CAST(revision AS TEXT)
			FROM mcp_files
			WHERE apikey_hash = ? AND project = ? AND path = ? AND deleted = FALSE AND system_owner = ?
			LIMIT 1`, s.isPostgres),
		apiKeyHash, project, path, owner,
	).Scan(
		&file.ID, &file.APIKeyHash, &file.Project, &file.Path, &file.Content,
		&file.Size, &file.CreatedAt, &file.UpdatedAt, &file.Deleted, &file.DeletedAt,
		&file.ContentHash, &file.SummaryContentHash, &file.SummaryStatus, &file.SummaryGenerationKey, &file.Version,
	)
	if err != nil {
		return nil, err
	}
	return &file, nil
}

// resolveActiveFile returns the live file a project path resolves to. File.Project
// names the physical project that stores it. sql.ErrNoRows means no visible file.
func (s *Service) resolveActiveFile(ctx context.Context, q sqlQueryer, apiKeyHash, project, path string) (*File, error) {
	file, err := s.findActiveFileQ(ctx, q, apiKeyHash, project, path)
	if err == nil || !errors.Is(err, sql.ErrNoRows) || !overlayApplies(ctx, project) || path == "" {
		return file, err
	}
	lower, err := s.findActiveFileQ(ctx, q, apiKeyHash, DefaultProject, path)
	if err != nil {
		return nil, err
	}
	hidden, err := s.upperHidesPath(ctx, q, apiKeyHash, project, path)
	if err != nil {
		return nil, err
	}
	if hidden {
		return nil, sql.ErrNoRows
	}
	return lower, nil
}

// upperHidesPath reports whether the upper project hides a lower file at path:
// it has a file at an ancestor of path, or any file below path.
func (s *Service) upperHidesPath(ctx context.Context, q sqlQueryer, apiKeyHash, upper, path string) (bool, error) {
	query := `SELECT COUNT(1) FROM mcp_files WHERE apikey_hash = ? AND project = ? AND deleted = FALSE AND system_owner = ?
		AND (path LIKE ?` + sqlLikeEscape
	args := []any{apiKeyHash, upper, systemOwnerFromContext(ctx), buildPathPrefix(path)}
	if parents := parentPaths(path); len(parents) > 0 {
		query += ` OR path IN (` + placeholders(len(parents)) + `)`
		for _, parent := range parents {
			args = append(args, parent)
		}
	}
	query += `)`
	var count int64
	if err := q.QueryRowContext(ctx, rebindSQL(query, s.isPostgres), args...).Scan(&count); err != nil {
		return false, errors.Wrap(err, "check upper layer shadow")
	}
	return count > 0, nil
}

// upperFileAtOrAbove reports whether the upper project has a file at path or at
// one of its ancestors, which hides the whole lower subtree rooted at path.
func (s *Service) upperFileAtOrAbove(ctx context.Context, q sqlQueryer, apiKeyHash, upper, path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	candidates := append(parentPaths(path), path)
	args := make([]any, 0, 3+len(candidates))
	args = append(args, apiKeyHash, upper, systemOwnerFromContext(ctx))
	for _, candidate := range candidates {
		args = append(args, candidate)
	}
	var count int64
	if err := q.QueryRowContext(ctx,
		rebindSQL(`SELECT COUNT(1) FROM mcp_files WHERE apikey_hash = ? AND project = ? AND deleted = FALSE AND system_owner = ?
			AND path IN (`+placeholders(len(candidates))+`)`, s.isPostgres),
		args...,
	).Scan(&count); err != nil {
		return false, errors.Wrap(err, "check upper layer subtree shadow")
	}
	return count > 0, nil
}

// layerDescendants returns the live file rows strictly below path in one physical project.
func (s *Service) layerDescendants(ctx context.Context, q sqlQueryer, apiKeyHash, project, path string) ([]layerFile, error) {
	rows, err := q.QueryContext(ctx,
		rebindSQL(`SELECT id, project, path, size, created_at, updated_at FROM mcp_files
			WHERE apikey_hash = ? AND project = ? AND path LIKE ? ESCAPE '\' AND deleted = FALSE AND system_owner = ?`, s.isPostgres),
		apiKeyHash, project, buildPathPrefix(path), systemOwnerFromContext(ctx),
	)
	if err != nil {
		return nil, errors.Wrap(err, "query descendant files")
	}
	defer func() { _ = rows.Close() }()
	var files []layerFile
	for rows.Next() {
		var row layerFile
		if err := rows.Scan(&row.ID, &row.Project, &row.Path, &row.Size, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, errors.Wrap(err, "scan descendant file")
		}
		files = append(files, row)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate descendant files")
	}
	return files, nil
}

// layerHasDescendants reports, with a bounded probe, whether one physical
// project has any live file strictly below path.
func (s *Service) layerHasDescendants(ctx context.Context, q sqlQueryer, apiKeyHash, project, path string) (bool, error) {
	var id uint64
	err := q.QueryRowContext(ctx,
		rebindSQL(`SELECT id FROM mcp_files
			WHERE apikey_hash = ? AND project = ? AND path LIKE ? ESCAPE '\' AND deleted = FALSE AND system_owner = ? LIMIT 1`, s.isPostgres),
		apiKeyHash, project, buildPathPrefix(path), systemOwnerFromContext(ctx),
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, errors.Wrap(err, "probe layer descendants")
	}
	return true, nil
}

// visibleDescendants returns the files below path that the project's merged view
// shows, each tagged with its physical project. Without an overlay this is exactly
// the project's own descendants.
func (s *Service) visibleDescendants(ctx context.Context, q sqlQueryer, apiKeyHash, project, path string) ([]layerFile, error) {
	upper, err := s.layerDescendants(ctx, q, apiKeyHash, project, path)
	if err != nil {
		return nil, err
	}
	if !overlayApplies(ctx, project) {
		return upper, nil
	}
	lower, err := s.layerDescendants(ctx, q, apiKeyHash, DefaultProject, path)
	if err != nil || len(lower) == 0 {
		return upper, err
	}
	hiddenSubtree, err := s.upperFileAtOrAbove(ctx, q, apiKeyHash, project, path)
	if err != nil || hiddenSubtree {
		return upper, err
	}
	upperPaths := make([]string, 0, len(upper))
	for _, row := range upper {
		upperPaths = append(upperPaths, row.Path)
	}
	shadow := newUpperShadow(upperPaths)
	merged := upper
	for _, row := range lower {
		if !shadow.hides(row.Path) {
			merged = append(merged, row)
		}
	}
	return merged, nil
}

// defaultLayerTouches reports whether the tenant's DefaultProject holds a live
// user file at, above, or below any of paths. A mutation of project paths the
// default layer does not touch neither reads nor changes that layer, so it keeps
// the single-project lock and the original physical code path. If a default file
// appears concurrently, the result equals running the mutation first.
func (s *Service) defaultLayerTouches(ctx context.Context, q sqlQueryer, apiKeyHash string, paths ...string) (bool, error) {
	exact := make([]string, 0, len(paths)*2)
	conditions := make([]string, 0, len(paths)+1)
	args := []any{apiKeyHash, DefaultProject, systemOwnerFromContext(ctx)}
	for _, path := range paths {
		conditions = append(conditions, `path LIKE ?`+sqlLikeEscape)
		args = append(args, buildPathPrefix(path))
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

// errOverlayRequired aborts a single-project attempt whose paths the default
// layer touches, so the mutation can restart under both project locks.
var errOverlayRequired = errors.New("mutation requires the default layer")

// runUserMutation runs fn in one locked transaction. An ordinary project first
// takes only its own lock and checks, inside that transaction, whether the
// default layer touches paths. If it does not, fn runs physically and never
// reads DefaultProject, which keeps the original single-project behavior. If it
// does, the attempt is rolled back before fn runs and fn reruns in overlay mode
// holding DefaultProject and project in ProjectLockOrder.
func (s *Service) runUserMutation(ctx context.Context, apiKeyHash, project string, paths []string, fn func(tx *sql.Tx, overlay bool) error) error {
	if !overlayApplies(ctx, project) {
		return s.lockProvider.WithProjectLock(ctx, s.db, s.isPostgres, apiKeyHash, project, s.settings.LockTimeout, func(tx *sql.Tx) error {
			return fn(tx, false)
		})
	}
	err := s.lockProvider.WithProjectLock(ctx, s.db, s.isPostgres, apiKeyHash, project, s.settings.LockTimeout, func(tx *sql.Tx) error {
		touches, err := s.defaultLayerTouches(ctx, tx, apiKeyHash, paths...)
		if err != nil {
			return err
		}
		if touches {
			return errOverlayRequired
		}
		return fn(tx, false)
	})
	if !errors.Is(err, errOverlayRequired) {
		return err
	}
	return s.withOverlayLock(ctx, apiKeyHash, project, func(tx *sql.Tx) error {
		return fn(tx, true)
	})
}

// withProjectsLock runs fn in one transaction holding every project scope in
// ProjectLockOrder. A provider without multi-scope support keeps its own first
// scope and the remaining scopes are acquired on the same transaction in order.
func (s *Service) withProjectsLock(ctx context.Context, apiKeyHash string, projects []string, fn func(tx *sql.Tx) error) error {
	if multi, ok := s.lockProvider.(MultiProjectLockProvider); ok {
		return multi.WithProjectsLock(ctx, s.db, s.isPostgres, apiKeyHash, projects, s.settings.LockTimeout, fn)
	}
	ordered := ProjectLockOrder(apiKeyHash, projects...)
	if len(ordered) == 0 {
		return errors.New("at least one project scope is required")
	}
	deadline := time.Now().Add(s.settings.LockTimeout)
	return s.lockProvider.WithProjectLock(ctx, s.db, s.isPostgres, apiKeyHash, ordered[0], s.settings.LockTimeout, func(tx *sql.Tx) error {
		for _, project := range ordered[1:] {
			if err := acquireProjectLock(ctx, tx, s.isPostgres, apiKeyHash, project, time.Until(deadline)); err != nil {
				return err
			}
		}
		return fn(tx)
	})
}

// withOverlayLock locks DefaultProject and project together for an overlay mutation.
func (s *Service) withOverlayLock(ctx context.Context, apiKeyHash, project string, fn func(tx *sql.Tx) error) error {
	return s.withProjectsLock(ctx, apiKeyHash, []string{DefaultProject, project}, fn)
}

// visibleFileAtAncestor reports whether the merged view has a file at any
// ancestor of path, which makes path unusable as a file or directory name.
func (s *Service) visibleFileAtAncestor(ctx context.Context, q sqlQueryer, apiKeyHash, project, path string) (bool, error) {
	for _, parent := range parentPaths(path) {
		_, err := s.resolveActiveFile(ctx, q, apiKeyHash, project, parent)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, errors.Wrap(err, "resolve ancestor file")
		}
	}
	return false, nil
}

// resolveWriteLayerTx selects the physical project an overlay write changes: the
// project of the visible file at path, otherwise the addressed project. A new
// file must also be a legal name in the merged view.
func (s *Service) resolveWriteLayerTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, path string) (string, error) {
	file, err := s.resolveActiveFile(ctx, tx, apiKeyHash, project, path)
	if err == nil {
		return file.Project, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", errors.Wrap(err, "resolve overlay write target")
	}
	descendants, err := s.visibleDescendants(ctx, tx, apiKeyHash, project, path)
	if err != nil {
		return "", err
	}
	if len(descendants) > 0 {
		return "", NewError(ErrCodeIsDirectory, "path has descendant files", false)
	}
	blocked, err := s.visibleFileAtAncestor(ctx, tx, apiKeyHash, project, path)
	if err != nil {
		return "", err
	}
	if blocked {
		return "", NewError(ErrCodeNotDirectory, "parent path is a file", false)
	}
	return project, nil
}

// effectiveVersionTx returns the version token of the file a project path
// resolves to, or "" when no file is visible there.
func (s *Service) effectiveVersionTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, path string) (string, error) {
	file, err := s.resolveActiveFile(ctx, tx, apiKeyHash, project, path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", errors.Wrap(err, "read effective file version")
	}
	return file.Version, nil
}

// checkEffectiveVersionTx enforces a caller condition against the file the
// merged view shows, never against a hidden layer.
func (s *Service) checkEffectiveVersionTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, path, expected string, createOnly bool) error {
	if expected == "" && !createOnly {
		return nil
	}
	actual, err := s.effectiveVersionTx(ctx, tx, apiKeyHash, project, path)
	if err != nil {
		return err
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
