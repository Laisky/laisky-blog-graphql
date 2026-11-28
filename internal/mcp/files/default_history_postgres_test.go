package files

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// newPostgresOverlayFixture returns an overlay fixture over a private
// PostgreSQL schema. It skips without FILEIO_TEST_POSTGRES_DSN.
func newPostgresOverlayFixture(t *testing.T) *overlayFixture {
	t.Helper()
	return &overlayFixture{
		t: t, ctx: context.Background(), svc: newPostgresViewService(t),
		auth:    AuthContext{APIKeyHash: "tenant-a", APIKey: "key-a", UserIdentity: "user:a"},
		foreign: AuthContext{APIKeyHash: "tenant-b", APIKey: "key-b", UserIdentity: "user:b"},
	}
}

// TestDefaultHistoryPostgres runs the history namespace, recovery, pagination
// and two-step restore scenarios on PostgreSQL.
func TestDefaultHistoryPostgres(t *testing.T) {
	f := newPostgresOverlayFixture(t)
	runDefaultHistoryNamespaceScenario(t, f)
	runDefaultHistoryRecoveryScenario(t, f)
	runDefaultHistoryReadConditionScenario(t, f)
}

// TestDefaultHistoryPostgresPageUsesOneSnapshot deletes the override between
// the namespace resolution and the history query of one page. The page must be
// the override's history as it was when the snapshot began, without the
// deletion's snapshot, which no instant showed under the override.
func TestDefaultHistoryPostgresPageUsesOneSnapshot(t *testing.T) {
	f := newPostgresOverlayFixture(t)
	f.put(DefaultProject, "/s.md", "s default")
	f.svc.mustWritePhysical(t, f.auth, "alpha", "/s.md", "s alpha v1")
	f.put("alpha", "/s.md", "s alpha v2")
	own := f.historyIDOf("alpha", "/s.md", "s alpha v1")

	var page HistoryPage
	require.NoError(t, f.svc.withReadSnapshot(f.ctx, true, func(q sqlQueryer) error {
		interleaved := &interleavingQueryer{sqlQueryer: q, interleave: func() { f.remove("alpha", "/s.md") }}
		var err error
		page, err = f.svc.versionPageQ(f.ctx, interleaved, f.auth.APIKeyHash, "alpha", "/s.md", 0, 200, true)
		require.Equal(t, 2, interleaved.statements, "precondition: resolution and query are two statements")
		return err
	}))
	require.Len(t, page.Versions, 1)
	require.Equal(t, strconv.FormatUint(own, 10), page.Versions[0].ID)

	// A new snapshot sees the committed deletion: the path resolves to the default.
	require.Equal(t, "s default", f.read("alpha", "/s.md"))
	require.Empty(t, f.history("alpha", "/s.md", DefaultProject))
}
