package pageindex

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/Laisky/zap/zaptest/observer"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// movableClock is a fake clock that tests advance explicitly.
type movableClock struct {
	mu  sync.Mutex
	now time.Time
}

// Now returns the current fake time.
func (c *movableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the fake clock forward by d.
func (c *movableClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// summaryPrompts counts the summary prompts a counting model received.
func summaryPrompts(llm *countingLLM) int {
	count := 0
	for _, prompt := range llm.Prompts() {
		if strings.Contains(prompt, "Partial Document Text") {
			count++
		}
	}
	return count
}

// pluginHarness is a started plugin over an in-memory database.
type pluginHarness struct {
	db     *sql.DB
	plugin *Plugin
	llm    *countingLLM
	spend  *SpendGovernor
}

// newPluginHarness starts a summarizing plugin whose rolling budget uses clock.
func newPluginHarness(t *testing.T, llm LLM, limits LimitsSettings, clock func() time.Time) *pluginHarness {
	t.Helper()
	db, service := newPersistenceTestService(t)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	settings := persistenceTestSettings()
	settings.Indexer.TimeoutIndex = time.Minute
	settings.Algo.GenerateNodeSummary = true
	settings.Limits = limits
	tokenizer, err := NewTokenizer(settings.LLM.IndexingModel)
	require.NoError(t, err)
	systemFS, err := service.SystemNamespace(SysOwner)
	require.NoError(t, err)
	spend := NewSpendGovernorWithClock(limits, clock, nil)
	plugin, err := New(PluginDeps{UserFS: service, SystemFS: systemFS, Settings: settings, LLM: llm, Tokenizer: tokenizer, Spend: spend})
	require.NoError(t, err)
	require.NoError(t, plugin.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, plugin.Stop(context.Background())) })
	h := &pluginHarness{db: db, plugin: plugin, spend: spend}
	if counting, ok := llm.(*countingLLM); ok {
		h.llm = counting
	}
	return h
}

// summaryStatus returns the user-row summary status and error code of path.
func (h *pluginHarness) summaryStatus(t *testing.T, auth files.AuthContext, project, path string) (string, string) {
	t.Helper()
	var status, code string
	require.NoError(t, h.db.QueryRowContext(context.Background(),
		`SELECT summary_status, summary_error_code FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = '' AND deleted = FALSE`,
		auth.APIKeyHash, project, path).Scan(&status, &code))
	return status, code
}

// entry returns the catalog entry of path.
func (h *pluginHarness) entry(t *testing.T, auth files.AuthContext, project, path string) IndexEntry {
	t.Helper()
	index, err := h.plugin.store.GetIndex(context.Background(), PageIndexStorageProject(auth, project))
	require.NoError(t, err)
	entry, ok := index[path]
	require.True(t, ok)
	return entry
}

// TestTenantBudgetIndependentOfWallClock covers review item 1 of #96: the tenant
// budget refilled from the wall clock, so a run slower than one refill interval
// (3.6 s at 1000 calls per hour) let an exhausted tenant reach the model again.
// With the budget's clock injected, a slow model changes nothing.
func TestTenantBudgetIndependentOfWallClock(t *testing.T) {
	clock := &movableClock{now: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)}
	llm := &slowCountingLLM{delay: 4 * time.Millisecond}
	h := newPluginHarness(t, llm, LimitsSettings{}, clock.Now)
	auth := files.AuthContext{APIKey: "key-a", APIKeyHash: "tenant-a", UserIdentity: "user:a"}
	for i := 0; i < 6; i++ {
		_, err := h.plugin.Write(context.Background(), auth, "project", fmt.Sprintf("/doc-%d.md", i), flatMarkdown(200, fmt.Sprintf("s%d", i)), "utf-8", 0, files.WriteModeTruncate)
		require.NoError(t, err)
	}
	require.Equal(t, int(DefaultTenantLLMCallsPerWindow), llm.Calls(), "the tenant ceiling holds however long the run takes")
}

// slowCountingLLM simulates a loaded machine: every call takes wall time.
type slowCountingLLM struct {
	countingLLM
	delay time.Duration
}

// Respond waits, then answers like countingLLM.
func (s *slowCountingLLM) Respond(ctx context.Context, req Request) (*Response, error) {
	time.Sleep(s.delay)
	return s.countingLLM.Respond(ctx, req)
}

// TestSlowCallDebtIsNotForgiven covers review item 5 of #96: a tenant bucket that
// refilled while its call was in flight was swept, and the late settle charged a
// detached bucket, forgiving the debt.
func TestSlowCallDebtIsNotForgiven(t *testing.T) {
	g, now := newTestGovernor(LimitsSettings{})
	ticket, ok := g.admit("t", 100)
	require.True(t, ok)
	*now = now.Add(60 * time.Second)
	other, ok := g.admit("other", 1)
	require.True(t, ok)
	other.settle(1)
	require.Equal(t, 2, g.Stats().TrackedTenants, "a bucket with a call in flight is not swept")
	ticket.settle(3_900_000)
	_, ok = g.admit("t", 3_000_000)
	require.False(t, ok, "a 3.9M-token charge leaves the tenant unable to spend 3M more")
	ticket.settle(3_900_000)
	_, ok = g.admit("t", 3_000_000)
	require.False(t, ok, "settling twice charges once")
}

// TestSpendGovernorReservesGlobalHeadroomForLightTenants checks the fairness
// rule: once the global budget reaches its reserve, only tenants that have spent
// less than one document's allowance this window are admitted.
func TestSpendGovernorReservesGlobalHeadroomForLightTenants(t *testing.T) {
	g, _ := newTestGovernor(LimitsSettings{GlobalLLMCallsPerWindow: 100, TenantLLMCallsPerWindow: 100, MaxLLMCallsPerDocument: 10, GlobalReservePercent: 20})
	for i := 0; i < 80; i++ {
		ticket, ok := g.admit("heavy", 1)
		require.True(t, ok, "call %d is outside the reserve", i)
		ticket.settle(1)
	}
	_, ok := g.admit("heavy", 1)
	require.False(t, ok, "a heavy tenant stops at the reserve")
	require.Equal(t, uint64(1), g.Stats().ReserveRefusals)
	for i := 0; i < 10; i++ {
		ticket, ok := g.admit("light", 1)
		require.True(t, ok, "a light tenant draws on the reserve")
		ticket.settle(1)
	}
	_, ok = g.admit("light", 1)
	require.False(t, ok, "a tenant draws at most one document's allowance from the reserve")
}

// TestHeavyTenantsCannotStarveLightTenant covers review item 3 of #96 end to end:
// five heavy keys exhausted the global budget and another tenant's first
// document got no generated summary. Now the reserve keeps that document whole,
// and the refusals are counted.
func TestHeavyTenantsCannotStarveLightTenant(t *testing.T) {
	clock := &movableClock{now: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)}
	h := newPluginHarness(t, &countingLLM{}, LimitsSettings{}, clock.Now)
	ctx := context.Background()
	for k := 0; k < 5; k++ {
		auth := files.AuthContext{APIKey: fmt.Sprintf("sk-heavy-%d", k), APIKeyHash: fmt.Sprintf("heavy-%d", k), UserIdentity: fmt.Sprintf("user:h%d", k)}
		for i := 0; i < 4; i++ {
			_, err := h.plugin.Write(ctx, auth, "p", fmt.Sprintf("/h-%d.md", i), flatMarkdown(256, fmt.Sprintf("h%d-%d", k, i)), "utf-8", 0, files.WriteModeTruncate)
			require.NoError(t, err)
		}
	}
	before := h.llm.Calls()
	light := files.AuthContext{APIKey: "sk-light", APIKeyHash: "light", UserIdentity: "user:light"}
	_, err := h.plugin.Write(ctx, light, "p", "/first.md", flatMarkdown(10, "light"), "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	require.Equal(t, 10, h.llm.Calls()-before, "a light tenant's first document keeps its generated summaries")
	stats := h.spend.Stats()
	t.Logf("heavy tenants spent %d calls; governor %+v", before, stats)
	require.LessOrEqual(t, before, int(DefaultGlobalLLMCallsPerWindow), "the absolute global ceiling still holds")
	require.Positive(t, stats.ReserveRefusals)
	require.Positive(t, stats.TenantRefusals+stats.GlobalRefusals+stats.ReserveRefusals, "every refusal is counted")
	status, _ := h.summaryStatus(t, light, "p", "/first.md")
	require.Equal(t, string(files.SummaryStatusReady), status)
}

// TestBudgetDegradedTreeIsPublishedDegradedAndRefreshed covers the degraded-status
// part of review item 3: a tree built with local summaries because a rolling
// budget was exhausted is published as degraded with a retryable state, and a
// search after the budget refills re-indexes it once.
func TestBudgetDegradedTreeIsPublishedDegradedAndRefreshed(t *testing.T) {
	clock := &movableClock{now: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)}
	h := newPluginHarness(t, &countingLLM{}, LimitsSettings{TenantLLMCallsPerWindow: 10, Window: time.Hour}, clock.Now)
	ctx := context.Background()
	auth := files.AuthContext{APIKey: "sk-a", APIKeyHash: "tenant-a", UserIdentity: "user:a"}
	_, err := h.plugin.Write(ctx, auth, "p", "/first.md", flatMarkdown(10, "f"), "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	_, err = h.plugin.Write(ctx, auth, "p", "/doc.md", flatMarkdown(5, "d"), "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	require.Equal(t, 10, summaryPrompts(h.llm), "the tenant budget is spent by the first document")

	status, code := h.summaryStatus(t, auth, "p", "/doc.md")
	require.Equal(t, string(files.SummaryStatusDegraded), status, "a tree built with local summaries is degraded, not ready")
	require.Equal(t, degradedBudget, code)
	entry := h.entry(t, auth, "p", "/doc.md")
	require.Equal(t, degradedBudget, entry.Degraded)
	require.Zero(t, entry.RefreshAttempts)
	require.NotEmpty(t, entry.RefreshAfter)

	_, err = h.plugin.Search(ctx, auth, "p", "topic", "", 5)
	require.NoError(t, err)
	require.Equal(t, 10, summaryPrompts(h.llm), "no refresh before it is due and while the budget is spent")

	clock.Advance(2 * time.Hour)
	for i := 0; i < 3; i++ {
		_, err = h.plugin.Search(ctx, auth, "p", "topic", "", 5)
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool {
		status, _ := h.summaryStatus(t, auth, "p", "/doc.md")
		return status == string(files.SummaryStatusReady)
	}, 10*time.Second, 10*time.Millisecond, "the refresh republishes a complete tree")
	require.Empty(t, h.entry(t, auth, "p", "/doc.md").Degraded)
	require.Equal(t, 10+5, summaryPrompts(h.llm), "repeated searches refresh the document once")
}

// TestDocumentLimitTreeIsDegradedButFinal checks that a tree degraded by the
// document's own ceiling is published as degraded but never refreshed, since a
// refresh would hit the same ceiling.
func TestDocumentLimitTreeIsDegradedButFinal(t *testing.T) {
	clock := &movableClock{now: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)}
	h := newPluginHarness(t, &countingLLM{}, LimitsSettings{MaxLLMCallsPerDocument: 2}, clock.Now)
	ctx := context.Background()
	auth := files.AuthContext{APIKey: "sk-a", APIKeyHash: "tenant-a", UserIdentity: "user:a"}
	_, err := h.plugin.Write(ctx, auth, "p", "/doc.md", flatMarkdown(5, "d"), "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	status, code := h.summaryStatus(t, auth, "p", "/doc.md")
	require.Equal(t, string(files.SummaryStatusDegraded), status)
	require.Equal(t, degradedDocumentLimit, code)
	entry := h.entry(t, auth, "p", "/doc.md")
	require.Equal(t, degradedDocumentLimit, entry.Degraded)
	require.Empty(t, entry.RefreshAfter)
	clock.Advance(24 * time.Hour)
	_, err = h.plugin.Search(ctx, auth, "p", "topic", "", 5)
	require.NoError(t, err)
	require.False(t, refreshDue(entry, clock.Now()))
	require.Equal(t, 2, summaryPrompts(h.llm))
}

// TestGovernorLogsRateLimitedRefusalSummary checks the operator signal: refusals,
// including the cheap pre-check, are summarized at most once per minute.
func TestGovernorLogsRateLimitedRefusalSummary(t *testing.T) {
	core, observed := observer.New(zap.DebugLevel)
	logger, err := logSDK.NewWithName("pageindex-spend", logSDK.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
	require.NoError(t, err)
	clock := &movableClock{now: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)}
	g := NewSpendGovernorWithClock(LimitsSettings{TenantLLMCallsPerWindow: 1}, clock.Now, logger)
	ticket, ok := g.admit("t", 1)
	require.True(t, ok)
	ticket.settle(1)
	clock.Advance(2 * time.Minute)
	require.True(t, g.wouldRefuse("t"))
	require.True(t, g.wouldRefuse("t"))
	require.Equal(t, 1, observed.FilterMessage("pageindex.spend.refusals").Len(), "one summary per minute")
	require.Equal(t, uint64(2), g.Stats().TenantRefusals)
	clock.Advance(2 * time.Minute)
	_, ok = g.admit("t", 1)
	require.False(t, ok)
	logs := observed.FilterMessage("pageindex.spend.refusals").All()
	require.Len(t, logs, 2)
	require.Equal(t, uint64(2), logs[1].ContextMap()["tenant_refusals"], "the summary covers every refusal since the last one")
}
