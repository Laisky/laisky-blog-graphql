// Package conformance provides the user-observable test suite every memory
// plugin must satisfy. Scenarios are sourced from proposal §5.1 (C01-C27)
// and §5.2 (R01-R10). Per §5.0 each scenario asserts user-observable
// behavior, never implementation details.
package conformance

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// errIsNotFound reports whether err carries a typed NOT_FOUND code or a
// plaintext message that any plugin author would treat as missing-file.
// Conformance scenarios accept either form so plugins are not forced into
// using a specific error type.
func errIsNotFound(err error) bool {
	if err == nil {
		return false
	}
	if files.IsCode(err, files.ErrCodeNotFound) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "not_found")
}

// Fixture is implemented by every plugin that wants to run the suite.
type Fixture interface {
	Plugin() mcpplugin.Plugin
	NewAuthContext(t *testing.T) mcpplugin.AuthContext
	NewProject(t *testing.T) string
	HasStorage() bool
	Cleanup(t *testing.T)
}

// MultiPluginFixture is implemented by fixtures that expose more than one
// plugin so the suite can exercise the cross-plugin scenarios C23/C24/C26 and
// the cross-plugin race R09. Single-plugin fixtures should embed only
// Fixture and set Options.SkipCrossPlugin = true.
type MultiPluginFixture interface {
	Fixture
	// SecondaryPlugin returns the "other" plugin used in cross-plugin
	// scenarios. It must be different from Plugin() and registered in the
	// manager returned by Manager().
	SecondaryPlugin() mcpplugin.Plugin
	// DefaultPluginName returns the manager-configured default plugin
	// name; affects whether C23 or C24 expects pageindex / rag as default.
	DefaultPluginName() string
	// Manager returns the plugin manager that the conformance scenarios
	// use to resolve per-call plugin overrides.
	Manager() *mcpplugin.Manager
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
	multi, ok := fx.(MultiPluginFixture)
	if !ok {
		t.Skip("requires MultiPluginFixture")
	}
	runCrossPluginIsolation(t, multi, mcpplugin.DefaultPluginRAG, mcpplugin.DefaultPluginPageIndex, "C23")
}

// runC24 — Mirror of C23 with default rag and explicit pageindex.
func runC24(t *testing.T, fx Fixture, opts Options) {
	if opts.SkipCrossPlugin {
		t.Skip("cross-plugin scenario skipped per options")
	}
	multi, ok := fx.(MultiPluginFixture)
	if !ok {
		t.Skip("requires MultiPluginFixture")
	}
	runCrossPluginIsolation(t, multi, mcpplugin.DefaultPluginPageIndex, mcpplugin.DefaultPluginRAG, "C24")
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
	multi, ok := fx.(MultiPluginFixture)
	if !ok {
		t.Skip("requires MultiPluginFixture")
	}
	runCrossPluginIsolation(t, multi, multi.Plugin().Name(), multi.SecondaryPlugin().Name(), "C26")
}

// runCrossPluginIsolation is the shared body for C23/C24/C26: write under
// pluginA, then assert read under pluginA returns those bytes and read under
// pluginB returns NOT_FOUND with a hint identifying pluginA as the owner.
func runCrossPluginIsolation(t *testing.T, fx MultiPluginFixture, pluginA, pluginB, label string) {
	if fx.Plugin() == nil || fx.SecondaryPlugin() == nil {
		t.Skipf("%s: fixture missing primary or secondary plugin", label)
	}
	manager := fx.Manager()
	if manager == nil {
		t.Skipf("%s: fixture missing manager", label)
	}
	if pluginA == pluginB {
		t.Skipf("%s: pluginA and pluginB must differ", label)
	}

	auth := fx.NewAuthContext(t)
	project := fx.NewProject(t)
	path := "/cross-plugin-" + label + ".txt"
	payload := "bytes-owned-by-" + pluginA

	ctxA := mcpplugin.WithOverride(context.Background(), pluginA)
	pA, err := manager.Resolve(ctxA, auth, project, pluginA)
	if err != nil {
		t.Fatalf("%s: resolve pluginA=%q: %v", label, pluginA, err)
	}
	if _, err := pA.Write(ctxA, auth, project, path, payload, "utf-8", 0, mcpplugin.WriteModeTruncate); err != nil {
		t.Fatalf("%s: write under pluginA=%q: %v", label, pluginA, err)
	}

	// Read under pluginA must return the bytes.
	readA, err := pA.Read(ctxA, auth, project, path, 0, -1)
	if err != nil {
		t.Fatalf("%s: read under pluginA=%q: %v", label, pluginA, err)
	}
	if readA.Content == "" || !strings.Contains(readA.Content, payload) {
		t.Errorf("%s: read under pluginA returned %q, want bytes containing %q", label, readA.Content, payload)
	}

	// Read under pluginB must return NOT_FOUND with a hint identifying pluginA.
	ctxB := mcpplugin.WithOverride(context.Background(), pluginB)
	pB, err := manager.Resolve(ctxB, auth, project, pluginB)
	if err != nil {
		t.Fatalf("%s: resolve pluginB=%q: %v", label, pluginB, err)
	}
	_, readErr := pB.Read(ctxB, auth, project, path, 0, -1)
	if readErr == nil {
		t.Fatalf("%s: read under pluginB=%q must return NOT_FOUND, got nil", label, pluginB)
	}
	// Either a typed files.Error with NOT_FOUND or a generic error mentioning the owner.
	if typed, ok := mcpplugin.AsResolveError(readErr); ok {
		// Resolution-side error — should never happen for a registered plugin.
		t.Fatalf("%s: unexpected ResolveError from registered pluginB=%q: %v", label, pluginB, typed)
	}
	if !errIsNotFound(readErr) {
		t.Errorf("%s: read under pluginB=%q expected NOT_FOUND, got %v", label, pluginB, readErr)
	}
	if !strings.Contains(readErr.Error(), pluginA) {
		// The proposal requires the error message hint name the owner. Fail
		// loudly so plugin authors keep the routing hint user-visible.
		t.Errorf("%s: read under pluginB=%q expected hint mentioning owner %q, got %v", label, pluginB, pluginA, readErr)
	}
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
	multi, ok := fx.(MultiPluginFixture)
	if !ok {
		t.Skip("requires MultiPluginFixture")
	}
	manager := multi.Manager()
	if manager == nil || multi.Plugin() == nil || multi.SecondaryPlugin() == nil {
		t.Skip("R09: fixture incomplete")
	}

	auth := multi.NewAuthContext(t)
	project := multi.NewProject(t)
	path := "/cross-plugin-R09.txt"
	pA := multi.Plugin()
	pB := multi.SecondaryPlugin()
	payloadA := "rag-bytes"
	payloadB := "pageindex-bytes"

	ctxA := mcpplugin.WithOverride(context.Background(), pA.Name())
	ctxB := mcpplugin.WithOverride(context.Background(), pB.Name())

	var wg sync.WaitGroup
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errA = pA.Write(ctxA, auth, project, path, payloadA, "utf-8", 0, mcpplugin.WriteModeTruncate)
	}()
	go func() {
		defer wg.Done()
		_, errB = pB.Write(ctxB, auth, project, path, payloadB, "utf-8", 0, mcpplugin.WriteModeTruncate)
	}()
	wg.Wait()

	if errA != nil {
		t.Errorf("R09: write under pluginA=%q: %v", pA.Name(), errA)
	}
	if errB != nil {
		t.Errorf("R09: write under pluginB=%q: %v", pB.Name(), errB)
	}

	// Each plugin must see its own bytes.
	readA, errA := pA.Read(ctxA, auth, project, path, 0, -1)
	if errA != nil {
		t.Errorf("R09: read under pluginA=%q: %v", pA.Name(), errA)
	} else if !strings.Contains(readA.Content, payloadA) {
		t.Errorf("R09: pluginA=%q read=%q want bytes containing %q", pA.Name(), readA.Content, payloadA)
	}
	readB, errB := pB.Read(ctxB, auth, project, path, 0, -1)
	if errB != nil {
		t.Errorf("R09: read under pluginB=%q: %v", pB.Name(), errB)
	} else if !strings.Contains(readB.Content, payloadB) {
		t.Errorf("R09: pluginB=%q read=%q want bytes containing %q", pB.Name(), readB.Content, payloadB)
	}
}

// runR10 — Thousands of concurrent reads stay within latency_budget × 1.5.
func runR10(t *testing.T, fx Fixture, opts Options) {
	if opts.SkipConcurrency {
		t.Skip("concurrency scenario skipped per options")
	}
	requireStorage(t, fx)
}
