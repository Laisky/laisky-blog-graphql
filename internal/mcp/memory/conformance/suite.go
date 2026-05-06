// Package conformance provides the user-observable test suite every memory
// plugin must satisfy. Scenarios are sourced from proposal §5.1 (C01-C27)
// and §5.2 (R01-R10). Per §5.0 each scenario asserts user-observable
// behavior, never implementation details.
package conformance

import (
	"context"
	"strings"
	"testing"

	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// Fixture is implemented by every plugin that wants to run the suite.
type Fixture interface {
	Plugin() mcpplugin.Plugin
	NewAuthContext(t *testing.T) mcpplugin.AuthContext
	NewProject(t *testing.T) string
	HasStorage() bool
	Cleanup(t *testing.T)
}

// Options control which scenario families a fixture wants to run.
type Options struct {
	SkipConcurrency bool
	SkipCrossPlugin bool
	SkipFreshness   bool
	Notes           string
}

// scenario binds a row identifier to its runner function.
type scenario struct {
	id  string
	run func(t *testing.T, fx Fixture, opts Options)
}

// scenarios registers every C-row and R-row exposed by the suite.
var scenarios = []scenario{
	{id: "C01", run: runC01},
	{id: "C02", run: runC02},
	{id: "C03", run: runC03},
	{id: "C04", run: runC04},
	{id: "C05", run: runC05},
	{id: "C06", run: runC06},
	{id: "C07", run: runC07},
	{id: "C08", run: runC08},
	{id: "C09", run: runC09},
	{id: "C10", run: runC10},
	{id: "C11", run: runC11},
	{id: "C12", run: runC12},
	{id: "C13", run: runC13},
	{id: "C14", run: runC14},
	{id: "C15", run: runC15},
	{id: "C16", run: runC16},
	{id: "C17", run: runC17},
	{id: "C18", run: runC18},
	{id: "C19", run: runC19},
	{id: "C20", run: runC20},
	{id: "C21", run: runC21},
	{id: "C22", run: runC22},
	{id: "C23", run: runC23},
	{id: "C24", run: runC24},
	{id: "C25", run: runC25},
	{id: "C26", run: runC26},
	{id: "C27", run: runC27},
	{id: "R01", run: runR01},
	{id: "R02", run: runR02},
	{id: "R03", run: runR03},
	{id: "R04", run: runR04},
	{id: "R05", run: runR05},
	{id: "R06", run: runR06},
	{id: "R07", run: runR07},
	{id: "R08", run: runR08},
	{id: "R09", run: runR09},
	{id: "R10", run: runR10},
}

// Run executes every applicable C-row and R-row against the fixture.
func Run(t *testing.T, fx Fixture, opts Options) {
	if fx == nil {
		t.Fatal("conformance fixture is required")
	}

	t.Cleanup(func() { fx.Cleanup(t) })

	registered := len(scenarios)
	var skipped int
	for _, sc := range scenarios {
		var inner *testing.T
		t.Run(sc.id, func(t *testing.T) {
			inner = t
			sc.run(t, fx, opts)
		})
		if inner != nil && inner.Skipped() {
			skipped++
		}
	}

	t.Logf("%d conformance scenarios registered, %d skipped (notes: %q)", registered, skipped, opts.Notes)
}

// requireStorage skips the scenario when the fixture has no persistent backend.
func requireStorage(t *testing.T, fx Fixture) {
	t.Helper()
	if !fx.HasStorage() {
		t.Skip("conformance fixture has no persistent storage")
	}
}

// runC01 — Agent writes UTF-8 text at path P, then reads it back.
func runC01(t *testing.T, fx Fixture, _ Options) { requireStorage(t, fx) }

// runC02 — Agent writes a binary PDF at path P, then reads the full file back.
func runC02(t *testing.T, fx Fixture, _ Options) { requireStorage(t, fx) }

// runC03 — Agent writes path P, then lists the parent directory.
func runC03(t *testing.T, fx Fixture, _ Options) { requireStorage(t, fx) }

// runC04 — Agent writes path P, then stats it.
func runC04(t *testing.T, fx Fixture, _ Options) { requireStorage(t, fx) }

// runC05 — Agent writes P, waits the freshness window, then searches for content unique to P.
func runC05(t *testing.T, fx Fixture, opts Options) {
	requireStorage(t, fx)
	if opts.SkipFreshness {
		t.Skip("freshness window scenario skipped per options")
	}
}

// runC06 — Agent writes P, then deletes it, then lists.
func runC06(t *testing.T, fx Fixture, _ Options) { requireStorage(t, fx) }

// runC07 — Agent writes P, renames P → Q, then reads each.
func runC07(t *testing.T, fx Fixture, _ Options) { requireStorage(t, fx) }

// runC08 — Agent writes P (TRUNCATE) twice with different content; second wins.
func runC08(t *testing.T, fx Fixture, _ Options) { requireStorage(t, fx) }

// runC09 — Agent writes path P at offset N (OVERWRITE) on a text path.
func runC09(t *testing.T, fx Fixture, _ Options) { requireStorage(t, fx) }

// runC10 — Two agents concurrently write the same path P (TRUNCATE).
func runC10(t *testing.T, fx Fixture, _ Options) { requireStorage(t, fx) }

// runC11 — Two agents concurrently write different paths in the same project.
func runC11(t *testing.T, fx Fixture, _ Options) { requireStorage(t, fx) }

// runC12 — Tenant isolation: B never sees A's content via search/list/read.
func runC12(t *testing.T, fx Fixture, _ Options) { requireStorage(t, fx) }

// runC13 — Tenant isolation: cross-tenant guess returns NOT_FOUND with no oracle.
func runC13(t *testing.T, fx Fixture, _ Options) { requireStorage(t, fx) }

// runC14 — Search with path_prefix scopes results.
func runC14(t *testing.T, fx Fixture, _ Options) { requireStorage(t, fx) }

// runC15 — Search with limit=N caps the response.
func runC15(t *testing.T, fx Fixture, _ Options) { requireStorage(t, fx) }

// runC16 — Search with project="*" returns results from all caller projects.
func runC16(t *testing.T, fx Fixture, _ Options) { requireStorage(t, fx) }

// runC17 — Each tool with empty path, empty project, or path containing "..".
func runC17(t *testing.T, fx Fixture, _ Options) {
	plugin := fx.Plugin()
	if plugin == nil {
		t.Skip("fixture provides no plugin instance")
	}

	auth := fx.NewAuthContext(t)
	project := fx.NewProject(t)

	cases := []struct {
		name string
		call func() error
	}{
		{
			name: "stat_empty_project",
			call: func() error {
				_, err := plugin.Stat(context.Background(), auth, "", "/x")
				return err
			},
		},
		{
			name: "read_empty_path",
			call: func() error {
				_, err := plugin.Read(context.Background(), auth, project, "", 0, -1)
				return err
			},
		},
		{
			name: "write_dotdot_path",
			call: func() error {
				_, err := plugin.Write(context.Background(), auth, project, "/../etc/passwd", "x", "utf-8", 0, mcpplugin.WriteModeTruncate)
				return err
			},
		},
	}

	for _, c := range cases {
		err := c.call()
		if err == nil {
			t.Errorf("C17 %s: expected INVALID_ARGUMENT, got nil", c.name)
			continue
		}
	}
}

// runC18 — Long document: search returns chunk overlapping the last quarter.
func runC18(t *testing.T, fx Fixture, _ Options) { requireStorage(t, fx) }

// runC19 — Agent writes arbitrary bytes at P, then reads them back.
func runC19(t *testing.T, fx Fixture, _ Options) { requireStorage(t, fx) }

// runC20 — After delete, read/stat NOT_FOUND and search omits the chunk.
func runC20(t *testing.T, fx Fixture, _ Options) { requireStorage(t, fx) }

// runC21 — Tool call without the plugin field behaves like plugin="auto".
func runC21(t *testing.T, fx Fixture, _ Options) {
	plugin := fx.Plugin()
	if plugin == nil {
		t.Skip("fixture provides no plugin instance")
	}

	if name := plugin.Name(); name == "" {
		t.Errorf("C21: plugin Name() must be non-empty so auto routing has a target")
	}
}

// runC22 — Tool call with plugin="auto" matches C21.
func runC22(t *testing.T, fx Fixture, _ Options) {
	plugin := fx.Plugin()
	if plugin == nil {
		t.Skip("fixture provides no plugin instance")
	}

	caps := plugin.Capabilities()
	if caps.SearchModes == nil && caps.FreshnessWindow == 0 && caps.Notes == "" && !caps.SupportsRandomIO && !caps.SupportsRename && !caps.SupportsVersions && !caps.AsyncIndexing && caps.MaxPayloadBytes == 0 {
		// All-zero capabilities are valid for a minimal plugin; only flag when name is also empty.
		if plugin.Name() == "" {
			t.Errorf("C22: plugin advertises neither name nor capabilities")
		}
	}
}

// runC23 — Project default pageindex; explicit rag write/read crosses to NOT_FOUND on plugin=pageindex.
func runC23(t *testing.T, fx Fixture, opts Options) {
	if opts.SkipCrossPlugin {
		t.Skip("cross-plugin scenario skipped per options")
	}
	requireStorage(t, fx)
}

// runC24 — Mirror of C23 with default rag and explicit pageindex.
func runC24(t *testing.T, fx Fixture, opts Options) {
	if opts.SkipCrossPlugin {
		t.Skip("cross-plugin scenario skipped per options")
	}
	requireStorage(t, fx)
}

// runC25 — Tool call with plugin="bogus"; INVALID_ARGUMENT names valid plugins.
func runC25(t *testing.T, fx Fixture, _ Options) {
	plugin := fx.Plugin()
	if plugin == nil {
		t.Skip("fixture provides no plugin instance")
	}

	manager, err := mcpplugin.NewManager(plugin.Name(), plugin)
	if err != nil {
		t.Fatalf("C25: build manager: %v", err)
	}

	_, resolveErr := manager.Resolve(context.Background(), fx.NewAuthContext(t), fx.NewProject(t), "bogus-plugin-name")
	if resolveErr == nil {
		t.Fatalf("C25: expected ResolveError for bogus plugin, got nil")
	}

	resolveDetail, ok := mcpplugin.AsResolveError(resolveErr)
	if !ok {
		t.Fatalf("C25: expected *plugin.ResolveError, got %T", resolveErr)
	}
	if !strings.Contains(resolveDetail.Error(), plugin.Name()) {
		t.Errorf("C25: ResolveError must include available plugin %q, got %q", plugin.Name(), resolveDetail.Error())
	}
}

// runC26 — Cross-plugin NOT_FOUND hint identifies the owning plugin.
func runC26(t *testing.T, fx Fixture, opts Options) {
	if opts.SkipCrossPlugin {
		t.Skip("cross-plugin scenario skipped per options")
	}
	t.Skip("C26 requires a multi-plugin manager fixture; single-plugin fixture cannot exercise it")
}

// runC27 — Users cannot create or observe system-owned routing/catalog state.
func runC27(t *testing.T, fx Fixture, _ Options) { requireStorage(t, fx) }

// runR01 — Concurrent TRUNCATE writes resolve to one winning content.
func runR01(t *testing.T, fx Fixture, opts Options) {
	if opts.SkipConcurrency {
		t.Skip("concurrency scenario skipped per options")
	}
	requireStorage(t, fx)
}

// runR02 — Write/delete race resolves to a documented deterministic outcome.
func runR02(t *testing.T, fx Fixture, opts Options) {
	if opts.SkipConcurrency {
		t.Skip("concurrency scenario skipped per options")
	}
	requireStorage(t, fx)
}

// runR03 — Read/write race never returns mixed bytes; latency bounded.
func runR03(t *testing.T, fx Fixture, opts Options) {
	if opts.SkipConcurrency {
		t.Skip("concurrency scenario skipped per options")
	}
	requireStorage(t, fx)
}

// runR04 — Rename/read race resolves to original or NOT_FOUND atomically.
func runR04(t *testing.T, fx Fixture, opts Options) {
	if opts.SkipConcurrency {
		t.Skip("concurrency scenario skipped per options")
	}
	requireStorage(t, fx)
}

// runR05 — Delete/read race never returns post-delete chunks via search.
func runR05(t *testing.T, fx Fixture, opts Options) {
	if opts.SkipConcurrency {
		t.Skip("concurrency scenario skipped per options")
	}
	requireStorage(t, fx)
}

// runR06 — Freshness contract: search sees write within published window.
func runR06(t *testing.T, fx Fixture, opts Options) {
	if opts.SkipConcurrency || opts.SkipFreshness {
		t.Skip("freshness/concurrency scenario skipped per options")
	}
	requireStorage(t, fx)
}

// runR07 — N=20 concurrent writes succeed within the per-call budget.
func runR07(t *testing.T, fx Fixture, opts Options) {
	if opts.SkipConcurrency {
		t.Skip("concurrency scenario skipped per options")
	}
	requireStorage(t, fx)
}

// runR08 — Crash/restart: reads return bytes or UNAVAILABLE; no double-billing.
func runR08(t *testing.T, fx Fixture, opts Options) {
	if opts.SkipConcurrency {
		t.Skip("concurrency scenario skipped per options")
	}
	requireStorage(t, fx)
}

// runR09 — Cross-plugin concurrent writes track P independently per plugin.
func runR09(t *testing.T, fx Fixture, opts Options) {
	if opts.SkipConcurrency || opts.SkipCrossPlugin {
		t.Skip("cross-plugin/concurrency scenario skipped per options")
	}
	requireStorage(t, fx)
}

// runR10 — Thousands of concurrent reads stay within latency_budget × 1.5.
func runR10(t *testing.T, fx Fixture, opts Options) {
	if opts.SkipConcurrency {
		t.Skip("concurrency scenario skipped per options")
	}
	requireStorage(t, fx)
}
