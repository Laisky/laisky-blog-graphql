package plugin

import (
	"context"
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
func (p *testPlugin) Rename(context.Context, files.AuthContext, string, string, string, bool) (files.RenameResult, error) {
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
	pageindexPlugin := &testPlugin{name: DefaultPluginPageIndex, statResult: files.StatResult{Exists: false}}

	mgr, err := NewManager(DefaultPluginRAG, ragPlugin, pageindexPlugin)
	require.NoError(t, err)

	resolved, err := mgr.Resolve(context.Background(), files.AuthContext{}, "demo", "")
	require.NoError(t, err)
	require.Equal(t, DefaultPluginRAG, resolved.Name())

	resolved, err = mgr.Resolve(context.Background(), files.AuthContext{}, "demo", DefaultPluginPageIndex)
	require.NoError(t, err)
	require.Equal(t, DefaultPluginPageIndex, resolved.Name())
}

// TestManagerResolveUnknownPlugin verifies user-correctable routing failures.
func TestManagerResolveUnknownPlugin(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(DefaultPluginRAG, &testPlugin{name: DefaultPluginRAG})
	require.NoError(t, err)

	_, err = mgr.Resolve(context.Background(), files.AuthContext{}, "demo", DefaultPluginPageIndex)
	require.Error(t, err)

	resolveErr, ok := AsResolveError(err)
	require.True(t, ok)
	require.Equal(t, DefaultPluginPageIndex, resolveErr.Requested)
	require.Equal(t, []string{DefaultPluginRAG}, resolveErr.Available)
}

// TestManagerRoutesViaContextOverride verifies the manager's FileService-compatible methods.
func TestManagerRoutesViaContextOverride(t *testing.T) {
	t.Parallel()

	ragPlugin := &testPlugin{name: DefaultPluginRAG, statResult: files.StatResult{Exists: true}}
	pageindexPlugin := &testPlugin{name: DefaultPluginPageIndex, statResult: files.StatResult{Exists: false}}

	mgr, err := NewManager(DefaultPluginRAG, ragPlugin, pageindexPlugin)
	require.NoError(t, err)

	result, err := mgr.Stat(WithOverride(context.Background(), DefaultPluginPageIndex), files.AuthContext{}, "demo", "/doc")
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
