package files

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
)

const projectsAPIPath = "/api/projects"

// handleListProjects accepts only search/pagination parameters. Caller-supplied
// tenant IDs, hashes, identities, and system owners are never used for discovery.
func (h *filesHTTPHandler) handleListProjects(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	logger := h.logFromCtx(ctx)
	if h.service == nil {
		h.writeErrorWithLogger(w, logger, http.StatusServiceUnavailable, "files service unavailable")
		return
	}
	authCtx, err := askuser.ParseAuthorizationFromContext(ctx, r.Header.Get("Authorization"))
	if err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusUnauthorized, "authorization required")
		return
	}
	query := r.URL.Query()
	limit := defaultProjectListLimit
	if values, ok := query["limit"]; ok {
		if len(values) != 1 {
			h.writeErrorWithLogger(w, logger, http.StatusBadRequest, "invalid project limit")
			return
		}
		limit, err = strconv.Atoi(values[0])
		if err != nil || limit < 1 || limit > maxProjectListLimit {
			h.writeErrorWithLogger(w, logger, http.StatusBadRequest, "project limit must be between 1 and 100")
			return
		}
	}
	result, err := h.service.ListProjects(ctx, toFilesAuth(authCtx), query.Get("q"), query.Get("after"), limit)
	if err != nil {
		h.writeFileError(w, logger, err, "list file projects")
		return
	}
	h.writeJSON(w, result)
}
