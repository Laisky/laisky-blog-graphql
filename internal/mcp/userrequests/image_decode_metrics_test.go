package userrequests

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/storage"
)

// TestExportMetricsPublishesDecodeBudgetUtilization pins the decode-budget
// gauges (maintainer review of PR #94, P3: the decode budget bounds decoding,
// not the process, so operators compare its utilization with
// process_resident_memory_bytes). The gauges read the decode budget of the
// manager that last exported its metrics, and every image collector is
// registered on the registry the API server serves.
func TestExportMetricsPublishesDecodeBudgetUtilization(t *testing.T) {
	t.Cleanup(func() { decodeBudgetSource.Store(nil) })
	decodeBudgetSource.Store(nil)
	require.Zero(t, testutil.ToFloat64(imageMetrics.decodeBudgetBytes), "no manager exported yet")

	settings := defaultImageSettings()
	settings.DecodeMemoryBudgetBytes = 123 << 20
	first := NewImageManager(storage.NewFakeStore(), nil, settings)
	first.ExportMetrics()
	require.InDelta(t, float64(123<<20), testutil.ToFloat64(imageMetrics.decodeBudgetBytes), 0)
	require.Zero(t, testutil.ToFloat64(imageMetrics.decodeInUseBytes))
	require.Zero(t, testutil.ToFloat64(imageMetrics.decodeWaiting))

	settings.DecodeMemoryBudgetBytes = 456 << 20
	NewImageManager(storage.NewFakeStore(), nil, settings).ExportMetrics()
	require.InDelta(t, float64(456<<20), testutil.ToFloat64(imageMetrics.decodeBudgetBytes), 0,
		"the gauges follow the manager that exported last")

	reg := prometheus.NewRegistry()
	newImageObjectMetrics().register(reg)
	families, err := reg.Gather()
	require.NoError(t, err)
	names := map[string]bool{}
	for _, family := range families {
		names[family.GetName()] = true
	}
	for _, name := range []string{
		"mcp_user_image_decode_budget_bytes",
		"mcp_user_image_decode_in_use_bytes",
		"mcp_user_image_decode_waiting",
	} {
		require.True(t, names[name], "%s must be registered", name)
	}
}
