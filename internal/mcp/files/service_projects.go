package files

import (
	"context"
	"strings"

	errors "github.com/Laisky/errors/v2"
)

const (
	defaultProjectListLimit = 50
	maxProjectListLimit     = 100
)

// ProjectListResult is a bounded page of the caller's live, user-visible projects.
// NextCursor is a project ID, not a tenant identifier or an authorization token.
type ProjectListResult struct {
	Projects   []string `json:"projects"`
	HasMore    bool     `json:"has_more"`
	NextCursor string   `json:"next_cursor,omitempty"`
}

// ListProjects discovers projects from live FileIO entries, not browser history.
// The authenticated API key and context-owned namespace constrain the query before
// DISTINCT, filtering, ordering, and pagination. A cursor never grants access.
func (s *Service) ListProjects(ctx context.Context, auth AuthContext, query, after string, limit int) (ProjectListResult, error) {
	if err := s.validateAuth(auth); err != nil {
		return ProjectListResult{}, errors.WithStack(err)
	}
	query = strings.TrimSpace(query)
	if query != "" {
		if err := ValidateProject(query); err != nil {
			return ProjectListResult{}, errors.WithStack(NewError(ErrCodeInvalidArgument, "invalid project search", false))
		}
	}
	if after != "" {
		if err := ValidateProject(after); err != nil {
			return ProjectListResult{}, errors.WithStack(NewError(ErrCodeInvalidArgument, "invalid project cursor", false))
		}
	}
	if limit == 0 {
		limit = defaultProjectListLimit
	}
	if limit < 1 || limit > maxProjectListLimit {
		return ProjectListResult{}, errors.WithStack(NewError(ErrCodeInvalidArgument, "project limit must be between 1 and 100", false))
	}

	rows, err := s.db.QueryContext(ctx, rebindSQL(`SELECT DISTINCT project FROM mcp_files
		WHERE apikey_hash = ? AND system_owner = ? AND deleted = FALSE
		AND project > ? AND LOWER(project) LIKE ? ESCAPE '\'
		ORDER BY project ASC LIMIT ?`, s.isPostgres),
		auth.APIKeyHash, systemOwnerFromContext(ctx), after, projectSearchPattern(query), limit+1)
	if err != nil {
		return ProjectListResult{}, errors.Wrap(err, "query file projects")
	}
	defer func() { _ = rows.Close() }()

	result := ProjectListResult{Projects: make([]string, 0, limit)}
	for rows.Next() {
		var project string
		if err := rows.Scan(&project); err != nil {
			return ProjectListResult{}, errors.Wrap(err, "scan file project")
		}
		if len(result.Projects) == limit {
			result.HasMore = true
			result.NextCursor = result.Projects[len(result.Projects)-1]
			break
		}
		result.Projects = append(result.Projects, project)
	}
	if err := rows.Err(); err != nil {
		return ProjectListResult{}, errors.Wrap(err, "iterate file projects")
	}
	return result, nil
}

// projectSearchPattern matches case-insensitive, ordered subsequences. For example,
// "mcp" matches "my-chat-project". Project punctuation remains literal, not SQL syntax.
func projectSearchPattern(query string) string {
	var pattern strings.Builder
	pattern.Grow(1 + len(query)*3)
	pattern.WriteByte('%')
	for _, char := range strings.ToLower(query) {
		if char == '_' || char == '%' || char == '\\' {
			pattern.WriteByte('\\')
		}
		pattern.WriteRune(char)
		pattern.WriteByte('%')
	}
	return pattern.String()
}
