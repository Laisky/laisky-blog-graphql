package files_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

type freshnessReranker struct{ docs []string }

func (r *freshnessReranker) Rerank(_ context.Context, _, _ string, docs []string) ([]float64, error) {
	r.docs = append(r.docs, docs...)
	out := make([]float64, len(docs))
	for i := range out {
		out[i] = 1
	}
	return out, nil
}

// TestSearchRenameFiltersStaleGenerations exercises both backend query engines
// with a pending/rejected UPSERT and with/without mandatory summaries.
func TestSearchRenameFiltersStaleGenerations(t *testing.T) {
	for _, backend := range credentialTestBackends {
		t.Run(backend, func(t *testing.T) {
			for _, enforce := range []bool{false, true} {
				for _, target := range []string{"race", "archive"} {
					checkSearchRenameFreshness(t, backend, target, enforce)
				}
			}
		})
	}
}

func checkSearchRenameFreshness(t *testing.T, backend, target string, enforce bool) {
	t.Helper()
	c := newCredentialFixture(t, backend, func(s *files.Settings) { s.Search.EnforceSummary = enforce })
	reranker := &freshnessReranker{}
	svc, err := files.NewService(c.db[0], c.settings, c.embedder, reranker, nil, nil, nil, c.clock.Now)
	require.NoError(t, err)
	const source, dest = "/source.txt", "/destination.txt"
	_, err = svc.Write(c.ctx, c.auth, "race", source, "current quasar", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(c.ctx, c.auth, target, dest, "old nebula", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	runWorker(t, c.ctx, svc)
	old, err := svc.Search(c.ctx, c.auth, target, "nebula", dest, 10)
	require.NoError(t, err)
	require.NotEmpty(t, old.Chunks)
	require.Contains(t, old.Chunks[0].ChunkContent, "nebula")
	_, err = svc.Rename(c.ctx, c.auth, files.RenameRequest{Project: "race", FromPath: source, TargetProject: target, ToPath: dest, Overwrite: true})
	require.NoError(t, err)
	check := func(wantCurrent bool) {
		t.Helper()
		reranker.docs = nil
		for _, scope := range []string{target, files.ProjectWildcard} {
			result, searchErr := svc.Search(c.ctx, c.auth, scope, "quasar nebula", dest, 10)
			require.NoError(t, searchErr)
			if wantCurrent {
				require.NotEmpty(t, result.Chunks)
			}
			for _, hit := range result.Chunks {
				require.Contains(t, hit.ChunkContent, "quasar")
				require.NotContains(t, hit.ChunkContent, "nebula")
			}
		}
		require.NotContains(t, strings.Join(reranker.docs, " "), "nebula", "stale chunks are rejected before ranking")
	}
	check(false)
	selectJob := " WHERE apikey_hash = ? AND project = ? AND file_path = ? AND operation = 'UPSERT' AND system_owner = ''"
	result, err := c.db[1].ExecContext(c.ctx, c.query("UPDATE mcp_file_index_jobs SET status = 'failed'"+selectJob+" AND status = 'pending'"), c.auth.APIKeyHash, target, dest)
	require.NoError(t, err)
	changed, err := result.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, changed)
	check(false)
	result, err = c.db[1].ExecContext(c.ctx, c.query("UPDATE mcp_file_index_jobs SET status = 'pending'"+selectJob+" AND status = 'failed'"), c.auth.APIKeyHash, target, dest)
	require.NoError(t, err)
	changed, err = result.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, changed)
	runWorker(t, c.ctx, svc)
	check(true)

	_, err = svc.WriteWith(c.ctx, c.auth, target, dest, "not for retrieval", "utf-8", 0, files.WriteModeTruncate, files.WriteOpts{SkipRAGIndex: true})
	require.NoError(t, err)
	hidden, err := svc.Search(c.ctx, c.auth, target, "retrieval", dest, 10)
	require.NoError(t, err)
	require.Empty(t, hidden.Chunks, "opt-out applies to indexed and raw fallback paths")
}
