package files

import (
	"context"
)

// searchScope selects the rows one file_search may return.
type searchScope struct {
	// project is the addressed project, or ProjectWildcard.
	project string
	// overlay also admits the DefaultProject rows that project does not hide.
	overlay bool
}

// resolveSearchScope decides once per search whether project overlays a
// non-empty DefaultProject. Tenants without default files keep the exact
// single-project predicate and query plans.
func (s *Service) resolveSearchScope(ctx context.Context, apiKeyHash, project string) (searchScope, error) {
	scope := searchScope{project: project}
	if !overlayApplies(ctx, project) {
		return scope, nil
	}
	hasDefaults, err := s.defaultLayerTouches(ctx, s.db, apiKeyHash, "")
	if err != nil {
		return searchScope{}, err
	}
	scope.overlay = hasDefaults
	return scope, nil
}

// chunkPredicate restricts the chunk alias "c" to the scope.
func (sc searchScope) chunkPredicate() (string, []any) {
	return sc.predicate("c.project", "c.file_path", "c.apikey_hash", "c.system_owner")
}

// predicate restricts a row to the scope. Overlay admits a DefaultProject row
// only when the addressed project has no live file at the same path, at one of
// its ancestors, or below it: the same visibility rule as stat, read and list.
// Paths are ASCII, so length/substr compare bytes on SQLite and PostgreSQL.
func (sc searchScope) predicate(projectCol, pathCol, tenantCol, ownerCol string) (string, []any) {
	switch {
	case sc.project == ProjectWildcard:
		return "", nil
	case !sc.overlay:
		return " AND " + projectCol + " = ?", []any{sc.project}
	}
	return ` AND (` + projectCol + ` = ? OR (` + projectCol + ` = ? AND NOT EXISTS (
		SELECT 1 FROM mcp_files o WHERE o.apikey_hash = ` + tenantCol + ` AND o.project = ? AND o.deleted = FALSE AND o.system_owner = ` + ownerCol + `
		AND (o.path = ` + pathCol + `
			OR substr(` + pathCol + `, 1, length(o.path) + 1) = o.path || '/'
			OR substr(o.path, 1, length(` + pathCol + `) + 1) = ` + pathCol + ` || '/'))))`,
		[]any{sc.project, DefaultProject, sc.project}
}

// jobPredicate restricts mcp_file_index_jobs rows to the scope's physical projects.
func (sc searchScope) jobPredicate() (string, []any) {
	switch {
	case sc.project == ProjectWildcard:
		return "", nil
	case !sc.overlay:
		return " AND project = ?", []any{sc.project}
	}
	return " AND project IN (?, ?)", []any{sc.project, DefaultProject}
}
