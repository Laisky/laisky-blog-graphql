package userrequests

import (
	"context"
	"fmt"
	"testing"
	"time"

	errs "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// TestPostgresReconcileTickDrainsAndReportsBacklog runs the reconciler tick
// on PostgreSQL (deliberate acceptance, MCP_USER_REQUESTS_TEST_POSTGRES_DSN):
// the drain loop and the backlog query, whose MIN(expires_at) the driver
// returns as a time.Time here and as text on SQLite.
func TestPostgresReconcileTickDrainsAndReportsBacklog(t *testing.T) {
	settings := defaultImageSettings()
	settings.OrphanSweepBatch = 2
	h := newPostgresOrphanHarness(t, settings, "")
	for i := range 5 {
		crashAfterUpload(t, h, fmt.Sprintf("sk-pg-drain-%d-1234567890", i), testPNG(t, 50+i, 9))
	}
	due := reconcileNow.Add(imageUploadClaimTTL)

	h.setClock(due.Add(time.Hour))
	h.store.DeleteErr = errs.New("injected delete failure")
	result, backlog, err := h.svc.ReconcileImageObjectsTick(context.Background(), h.manager())
	require.Error(t, err)
	require.Equal(t, 1, result.Passes)
	require.EqualValues(t, 3, backlog.Due)
	require.Equal(t, time.Hour, backlog.OldestDueAge)

	h.store.DeleteErr = nil
	h.setClock(due.Add(time.Hour + imageDeleteLeaseTTL))
	result, backlog, err = h.svc.ReconcileImageObjectsTick(context.Background(), h.manager())
	require.NoError(t, err)
	require.Equal(t, 5, result.Deleted)
	require.Equal(t, 3, result.Passes)
	require.Zero(t, backlog.Due)
	require.Zero(t, backlog.OldestDueAge)
	require.Empty(t, h.store.Keys())
}
