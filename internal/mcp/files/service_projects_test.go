package files_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestProjectDiscoveryBehavior exercises real storage and the authenticated HTTP
// boundary; optional PostgreSQL runs use the same acceptance cases as SQLite.
func TestProjectDiscoveryBehavior(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			f := newRaceFixture(t, backend, nil)
			a, err := mcpauth.DeriveFromAPIKey("sk-project-owner-a")
			require.NoError(t, err)
			b, err := mcpauth.DeriveFromAPIKey("sk-project-owner-b")
			require.NoError(t, err)
			authA := files.AuthContext{APIKeyHash: a.APIKeyHash, UserID: b.UserID, UserIdentity: b.UserIdentity}
			authB := files.AuthContext{APIKeyHash: b.APIKeyHash}
			write := func(auth files.AuthContext, project, path string) {
				t.Helper()
				_, writeErr := f.svc[0].Write(f.ctx, auth, project, path, "content", "utf-8", 0, files.WriteModeTruncate)
				require.NoError(t, writeErr)
			}
			for _, project := range []string{"Alpha", "literal_under", "my-chat-project", "shared", "zeta", "deleted-only", "internal-only"} {
				write(authA, project, "/one.txt")
			}
			write(authA, "Alpha", "/two.txt")  // Multiple files yield one suggestion.
			write(authB, "shared", "/one.txt") // Identical IDs do not share ownership.
			write(authB, "b-private", "/one.txt")
			_, err = f.db[0].ExecContext(f.ctx, f.query(`UPDATE mcp_files SET deleted = TRUE
				WHERE apikey_hash = ? AND project = ? AND system_owner = ?`), a.APIKeyHash, "deleted-only", "")
			require.NoError(t, err)
			_, err = f.db[0].ExecContext(f.ctx, f.query(`UPDATE mcp_files SET system_owner = ?
				WHERE apikey_hash = ? AND project = ? AND system_owner = ?`), "pageindex", a.APIKeyHash, "internal-only", "")
			require.NoError(t, err)

			t.Run("distinct live projects in the caller namespace", func(t *testing.T) {
				page, listErr := f.svc[0].ListProjects(f.ctx, authA, "", "", 0)
				require.NoError(t, listErr)
				require.Equal(t, []string{"Alpha", "literal_under", "my-chat-project", "shared", "zeta"}, page.Projects)
				require.False(t, page.HasMore)
				require.Empty(t, page.NextCursor)
				page, listErr = f.svc[0].ListProjects(f.ctx, authB, "", "", 100)
				require.NoError(t, listErr)
				require.Equal(t, []string{"b-private", "shared"}, page.Projects)
			})

			t.Run("case insensitive subsequences and literal punctuation", func(t *testing.T) {
				for query, expected := range map[string][]string{
					"MCP": {"my-chat-project"}, "LPH": {"Alpha"}, "_": {"literal_under"},
					"shared": {"shared"}, "b-private": {}, "deleted-only": {}, "internal-only": {},
				} {
					page, listErr := f.svc[0].ListProjects(f.ctx, authA, query, "", 100)
					require.NoError(t, listErr, query)
					require.Equal(t, expected, page.Projects, query)
					require.False(t, page.HasMore)
				}
			})

			t.Run("bounded pagination cannot widen ownership", func(t *testing.T) {
				var collected []string
				cursor := ""
				for range 5 {
					page, listErr := f.svc[0].ListProjects(f.ctx, authA, "", cursor, 2)
					require.NoError(t, listErr)
					require.LessOrEqual(t, len(page.Projects), 2)
					collected = append(collected, page.Projects...)
					if !page.HasMore {
						require.Empty(t, page.NextCursor)
						break
					}
					require.Equal(t, page.Projects[len(page.Projects)-1], page.NextCursor)
					require.Greater(t, page.NextCursor, cursor)
					cursor = page.NextCursor
				}
				require.Equal(t, []string{"Alpha", "literal_under", "my-chat-project", "shared", "zeta"}, collected)
				page, listErr := f.svc[0].ListProjects(f.ctx, authB, "", "Alpha", 1)
				require.NoError(t, listErr)
				require.Equal(t, []string{"b-private"}, page.Projects)
				page, listErr = f.svc[0].ListProjects(f.ctx, authA, "shared", "", 1)
				require.NoError(t, listErr)
				require.False(t, page.HasMore, "the other tenant's matching row must not affect pagination")
			})

			t.Run("fail closed for missing auth and invalid arguments", func(t *testing.T) {
				_, listErr := f.svc[0].ListProjects(f.ctx, files.AuthContext{}, "", "", 10)
				require.Error(t, listErr)
				for _, args := range []struct {
					query, cursor string
					limit         int
				}{
					{query: "%"}, {query: "' OR 1=1 --"}, {query: strings.Repeat("a", 129)},
					{cursor: "../x"}, {cursor: strings.Repeat("x", 129)}, {limit: -1}, {limit: 101},
				} {
					_, listErr = f.svc[0].ListProjects(f.ctx, authA, args.query, args.cursor, args.limit)
					require.Error(t, listErr)
				}
				ctx, cancel := context.WithCancel(f.ctx)
				cancel()
				_, listErr = f.svc[0].ListProjects(ctx, authA, "", "", 10)
				require.ErrorIs(t, listErr, context.Canceled)
			})

			t.Run("HTTP derives identity from credentials and never caches metadata", func(t *testing.T) {
				handler := files.NewHTTPHandler(f.svc[0], nil)
				request := func(header, query string, status int) *httptest.ResponseRecorder {
					t.Helper()
					req := httptest.NewRequest(http.MethodGet, "/api/projects"+query, nil)
					if header != "" {
						req.Header.Set("Authorization", header)
					}
					req.Header.Set("X-User-ID", b.UserID)
					req.Header.Set("X-APIKey-Hash", b.APIKeyHash)
					req.Header.Set("X-System-Owner", "pageindex")
					// Even a prepopulated foreign context must lose to the actual header.
					req = req.WithContext(mcpauth.WithContext(req.Context(), b))
					rr := httptest.NewRecorder()
					handler.ServeHTTP(rr, req)
					require.Equal(t, status, rr.Code, rr.Body.String())
					require.Contains(t, rr.Header().Get("Cache-Control"), "no-store")
					require.Contains(t, rr.Header().Get("Vary"), "Authorization")
					return rr
				}
				request("", "", http.StatusUnauthorized)
				for _, query := range []string{"?limit=0", "?limit=101", "?limit=-1", "?limit=x", "?limit=1&limit=2", "?q=%25", "?after=..%2Fx"} {
					request("Bearer "+a.APIKey, query, http.StatusBadRequest)
				}
				spoof := "?q=b-private&apikey_hash=" + b.APIKeyHash + "&user_id=" + url.QueryEscape(b.UserID) + "&system_owner=pageindex"
				for _, header := range []string{a.APIKey, "Bearer " + a.APIKey, "Bearer user:victim@" + a.APIKey} {
					rr := request(header, spoof, http.StatusOK)
					require.JSONEq(t, `{"projects":[],"has_more":false}`, rr.Body.String())
				}
				rr := request("Bearer "+b.APIKey, "?limit=1", http.StatusOK)
				var page files.ProjectListResult
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &page))
				require.Equal(t, []string{"b-private"}, page.Projects)
				require.True(t, page.HasMore)
				require.Equal(t, "b-private", page.NextCursor)
				rr = request("Bearer sk-unknown-owner", "", http.StatusOK)
				require.JSONEq(t, `{"projects":[],"has_more":false}`, rr.Body.String())
				require.NotContains(t, rr.Body.String(), b.APIKeyHash)
			})
		})
	}
}
