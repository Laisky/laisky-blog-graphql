package plugin

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// testPlugin is a lightweight plugin stub for manager tests.
type testPlugin struct {
	name        string
	startCalled int
	stopCalled  int
	statResult  files.StatResult
	statErr     error
}

// Name returns the plugin name.
func (p *testPlugin) Name() string { return p.name }

// Capabilities returns stub capabilities.
func (p *testPlugin) Capabilities() Capabilities {
	return Capabilities{FreshnessWindow: time.Second}
}

// Start records startup calls.
func (p *testPlugin) Start(context.Context) error {
	p.startCalled++
	return nil
}

// Stop records stop calls.
func (p *testPlugin) Stop(context.Context) error {
	p.stopCalled++
	return nil
}

// Stat returns the configured stat response.
func (p *testPlugin) Stat(context.Context, files.AuthContext, string, string) (files.StatResult, error) {
	return p.statResult, p.statErr
}

// Read returns a zero-value stub response.
func (p *testPlugin) Read(context.Context, files.AuthContext, string, string, int64, int64) (files.ReadResult, error) {
	return files.ReadResult{}, nil
}

// Write returns a zero-value stub response.
func (p *testPlugin) Write(context.Context, files.AuthContext, string, string, string, string, int64, files.WriteMode) (files.WriteResult, error) {
	return files.WriteResult{}, nil
}

// Delete returns a zero-value stub response.
func (p *testPlugin) Delete(context.Context, files.AuthContext, string, string, bool) (files.DeleteResult, error) {
	return files.DeleteResult{}, nil
}

// Rename returns a zero-value stub response.
func (p *testPlugin) Rename(context.Context, files.AuthContext, files.RenameRequest) (files.RenameResult, error) {
	return files.RenameResult{}, nil
}

// List returns a zero-value stub response.
func (p *testPlugin) List(context.Context, files.AuthContext, string, string, int, int) (files.ListResult, error) {
	return files.ListResult{}, nil
}

// Search returns a zero-value stub response.
func (p *testPlugin) Search(context.Context, files.AuthContext, string, string, string, int) (files.SearchResult, error) {
	return files.SearchResult{}, nil
}

// TestManagerResolve verifies default and explicit plugin routing.
func TestManagerResolve(t *testing.T) {
	t.Parallel()

	ragPlugin := &testPlugin{name: DefaultPluginRAG, statResult: files.StatResult{Exists: true}}
	secondary := &testPlugin{name: "secondary", statResult: files.StatResult{Exists: false}}

	mgr, err := NewManager(DefaultPluginRAG, ragPlugin, secondary)
	require.NoError(t, err)

	resolved, err := mgr.Resolve(context.Background(), files.AuthContext{}, "demo", "")
	require.NoError(t, err)
	require.Equal(t, DefaultPluginRAG, resolved.Name())

	resolved, err = mgr.Resolve(context.Background(), files.AuthContext{}, "demo", "secondary")
	require.NoError(t, err)
	require.Equal(t, "secondary", resolved.Name())
}

// TestManagerResolveUnknownPlugin verifies user-correctable routing failures.
func TestManagerResolveUnknownPlugin(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(DefaultPluginRAG, &testPlugin{name: DefaultPluginRAG})
	require.NoError(t, err)

	_, err = mgr.Resolve(context.Background(), files.AuthContext{}, "demo", "bogus")
	require.Error(t, err)

	resolveErr, ok := AsResolveError(err)
	require.True(t, ok)
	require.Equal(t, "bogus", resolveErr.Requested)
	require.Equal(t, []string{DefaultPluginRAG}, resolveErr.Available)
	require.Equal(t, `plugin "bogus" is not available; available plugins: rag`, resolveErr.Error())
}

// TestManagerRefusesDisabledPageIndex verifies that PageIndex is never routed
// to: an explicit or context selection fails with the shared disabled
// message, even when a plugin of that name was registered, and a stale
// pageindex default is refused instead of silently activating it.
func TestManagerRefusesDisabledPageIndex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ragPlugin := &testPlugin{name: DefaultPluginRAG, statResult: files.StatResult{Exists: true}}
	stale := &renameRecordingPlugin{testPlugin: testPlugin{name: DefaultPluginPageIndex}}

	for _, mgr := range []*Manager{
		mustManager(t, DefaultPluginRAG, ragPlugin),
		mustManager(t, DefaultPluginRAG, ragPlugin, stale),
	} {
		requireDisabled := func(err error) {
			t.Helper()
			resolveErr, ok := AsResolveError(err)
			require.True(t, ok, "%v", err)
			require.Equal(t, DefaultPluginPageIndex, resolveErr.Requested)
			require.Equal(t, PageIndexDisabledMessage, resolveErr.Error())
			require.Contains(t, resolveErr.Available, DefaultPluginRAG)
		}
		_, err := mgr.Resolve(ctx, files.AuthContext{}, "demo", " PageIndex ")
		requireDisabled(err)
		_, err = mgr.ForName(DefaultPluginPageIndex)
		requireDisabled(err)
		selected := WithOverride(ctx, DefaultPluginPageIndex)
		_, err = mgr.Stat(selected, files.AuthContext{}, "demo", "/doc")
		requireDisabled(err)
		_, err = mgr.Write(selected, files.AuthContext{}, "demo", "/doc", "x", "utf-8", 0, files.WriteModeTruncate)
		requireDisabled(err)
		_, err = mgr.Rename(selected, files.AuthContext{}, files.RenameRequest{Project: "demo", FromPath: "/a.md", ToPath: "/a.pdf"})
		requireDisabled(err)

		resolved, err := mgr.Resolve(WithOverride(ctx, DefaultPluginAuto), files.AuthContext{}, "demo", "")
		require.NoError(t, err)
		require.Same(t, ragPlugin, resolved)
	}
	require.Empty(t, stale.recorded(), "a registered pageindex plugin is never called")

	_, err := NewManager(" pageindex ", ragPlugin)
	require.ErrorContains(t, err, PageIndexDisabledMessage)
}

// mustManager builds a manager or fails the test.
func mustManager(t *testing.T, defaultPlugin string, plugins ...Plugin) *Manager {
	t.Helper()
	mgr, err := NewManager(defaultPlugin, plugins...)
	require.NoError(t, err)
	return mgr
}

// TestManagerRoutesViaContextOverride verifies the manager's FileService-compatible methods.
func TestManagerRoutesViaContextOverride(t *testing.T) {
	t.Parallel()

	ragPlugin := &testPlugin{name: DefaultPluginRAG, statResult: files.StatResult{Exists: true}}
	secondary := &testPlugin{name: "secondary", statResult: files.StatResult{Exists: false}}

	mgr, err := NewManager(DefaultPluginRAG, ragPlugin, secondary)
	require.NoError(t, err)

	result, err := mgr.Stat(WithOverride(context.Background(), "secondary"), files.AuthContext{}, "demo", "/doc")
	require.NoError(t, err)
	require.False(t, result.Exists)
}

// TestManagerStartAll verifies plugin lifecycle forwarding.
func TestManagerStartAll(t *testing.T) {
	t.Parallel()

	ragPlugin := &testPlugin{name: DefaultPluginRAG}
	mgr, err := NewManager(DefaultPluginRAG, ragPlugin)
	require.NoError(t, err)

	require.NoError(t, mgr.StartAll(context.Background()))
	require.NoError(t, mgr.StopAll(context.Background()))
	require.Equal(t, 1, ragPlugin.startCalled)
	require.Equal(t, 1, ragPlugin.stopCalled)
}

// renameRecordingPlugin records every rename request it receives.
type renameRecordingPlugin struct {
	testPlugin
	mu       sync.Mutex
	requests []files.RenameRequest
}

// Rename records req.
func (p *renameRecordingPlugin) Rename(_ context.Context, _ files.AuthContext, req files.RenameRequest) (files.RenameResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	return files.RenameResult{MovedCount: 1}, nil
}

// recorded returns a copy of the recorded requests.
func (p *renameRecordingPlugin) recorded() []files.RenameRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]files.RenameRequest(nil), p.requests...)
}

// TestRenameRoutingForwardsTheWholeRequest verifies that the manager passes
// one rename request, including its target project, unchanged to exactly the
// selected plugin: there is no separate cross-project entry point to miss and
// no second plugin that receives a copy of the move.
func TestRenameRoutingForwardsTheWholeRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	move := files.RenameRequest{Project: "alpha", FromPath: "/a.md", TargetProject: "beta", ToPath: "/b.md", Overwrite: true}
	same := files.RenameRequest{Project: "alpha", FromPath: "/a.md", ToPath: "/b.md"}

	rag := &renameRecordingPlugin{testPlugin: testPlugin{name: DefaultPluginRAG}}
	secondary := &renameRecordingPlugin{testPlugin: testPlugin{name: "secondary"}}
	mgr, err := NewManager(DefaultPluginRAG, rag, secondary)
	require.NoError(t, err)
	for _, req := range []files.RenameRequest{move, same} {
		result, err := mgr.Rename(ctx, files.AuthContext{}, req)
		require.NoError(t, err)
		require.Equal(t, 1, result.MovedCount)
	}
	_, err = mgr.Rename(WithOverride(ctx, "secondary"), files.AuthContext{}, move)
	require.NoError(t, err)
	require.Equal(t, []files.RenameRequest{move, same}, rag.recorded())
	require.Equal(t, []files.RenameRequest{move}, secondary.recorded())
}
