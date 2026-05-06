package rag

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/conformance"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// Compile-time assertion that *Plugin satisfies mcpplugin.Plugin.
var _ mcpplugin.Plugin = (*Plugin)(nil)

// TestPluginInterfaceContract asserts the rag plugin honors the Plugin contract surface.
func TestPluginInterfaceContract(t *testing.T) {
	t.Parallel()

	p := &Plugin{}
	require.Equal(t, mcpplugin.DefaultPluginRAG, p.Name())
	caps := p.Capabilities()
	require.NotEmpty(t, caps.SearchModes)
}

// TestPluginCapabilities pins the user-visible capability matrix for the rag plugin.
func TestPluginCapabilities(t *testing.T) {
	t.Parallel()

	p := &Plugin{}
	caps := p.Capabilities()
	require.Contains(t, caps.SearchModes, mcpplugin.SearchModeHybrid)
	require.Contains(t, caps.SearchModes, mcpplugin.SearchModeSemantic)
	require.Contains(t, caps.SearchModes, mcpplugin.SearchModeLexical)
	require.True(t, caps.SupportsRandomIO)
	require.True(t, caps.SupportsRename)
	require.True(t, caps.SupportsVersions)
	require.True(t, caps.AsyncIndexing)
	require.Equal(t, 5*time.Second, caps.FreshnessWindow)
}

// TestPluginName asserts the plugin reports the rag identifier.
func TestPluginName(t *testing.T) {
	t.Parallel()

	p := &Plugin{}
	require.Equal(t, "rag", p.Name())
}

// TestPluginConformance_NoStorage runs the conformance suite against a placeholder fixture.
func TestPluginConformance_NoStorage(t *testing.T) {
	t.Parallel()

	conformance.Run(t, &noStorageFixture{}, conformance.Options{
		SkipConcurrency: true,
		SkipCrossPlugin: true,
		SkipFreshness:   true,
		Notes:           "rag plugin without persistent storage",
	})
}

// noStorageFixture exercises the conformance wiring without a database.
type noStorageFixture struct{}

// Plugin returns a stub plugin that advertises rag identity and rejects mutating calls.
func (f *noStorageFixture) Plugin() mcpplugin.Plugin { return &noStoragePlugin{} }

// NewAuthContext returns a synthetic caller identity scoped to the test.
func (f *noStorageFixture) NewAuthContext(t *testing.T) mcpplugin.AuthContext {
	t.Helper()
	return mcpplugin.AuthContext{APIKey: "test-key", APIKeyHash: "test-hash", UserIdentity: "user:" + t.Name()}
}

// NewProject returns a synthetic project name scoped to the test.
func (f *noStorageFixture) NewProject(t *testing.T) string {
	t.Helper()
	return "conf-" + t.Name()
}

// HasStorage reports false so storage-dependent scenarios skip cleanly.
func (f *noStorageFixture) HasStorage() bool { return false }

// Cleanup is a no-op for the no-storage fixture.
func (f *noStorageFixture) Cleanup(*testing.T) {}

// noStoragePlugin is a contract-satisfying stand-in for the conformance harness.
type noStoragePlugin struct{}

// Name returns the rag plugin identifier.
func (p *noStoragePlugin) Name() string { return mcpplugin.DefaultPluginRAG }

// Capabilities advertises the rag plugin's user-visible behavior matrix.
func (p *noStoragePlugin) Capabilities() mcpplugin.Capabilities {
	return mcpplugin.Capabilities{
		SearchModes:      []mcpplugin.SearchMode{mcpplugin.SearchModeHybrid},
		SupportsRandomIO: true,
		SupportsRename:   true,
		SupportsVersions: true,
		AsyncIndexing:    true,
		FreshnessWindow:  5 * time.Second,
	}
}

// Start is a no-op for the no-storage stand-in.
func (p *noStoragePlugin) Start(context.Context) error { return nil }

// Stop is a no-op for the no-storage stand-in.
func (p *noStoragePlugin) Stop(context.Context) error { return nil }

// Stat rejects empty project arguments with INVALID_ARGUMENT.
func (p *noStoragePlugin) Stat(_ context.Context, _ files.AuthContext, project, path string) (files.StatResult, error) {
	return files.StatResult{}, validatePath(project, path)
}

// Read rejects empty paths with INVALID_ARGUMENT.
func (p *noStoragePlugin) Read(_ context.Context, _ files.AuthContext, project, path string, _, _ int64) (files.ReadResult, error) {
	return files.ReadResult{}, validatePath(project, path)
}

// Write rejects ".." traversal with INVALID_ARGUMENT.
func (p *noStoragePlugin) Write(_ context.Context, _ files.AuthContext, project, path, _, _ string, _ int64, _ files.WriteMode) (files.WriteResult, error) {
	return files.WriteResult{}, validatePath(project, path)
}

// Delete is a no-op for the stand-in.
func (p *noStoragePlugin) Delete(context.Context, files.AuthContext, string, string, bool) (files.DeleteResult, error) {
	return files.DeleteResult{}, nil
}

// Rename is a no-op for the stand-in.
func (p *noStoragePlugin) Rename(context.Context, files.AuthContext, string, string, string, bool) (files.RenameResult, error) {
	return files.RenameResult{}, nil
}

// List is a no-op for the stand-in.
func (p *noStoragePlugin) List(context.Context, files.AuthContext, string, string, int, int) (files.ListResult, error) {
	return files.ListResult{}, nil
}

// Search is a no-op for the stand-in.
func (p *noStoragePlugin) Search(context.Context, files.AuthContext, string, string, string, int) (files.SearchResult, error) {
	return files.SearchResult{}, nil
}

// validatePath enforces non-empty project/path and rejects ".." traversal.
func validatePath(project, path string) error {
	if project == "" {
		return files.NewError(files.ErrCodeInvalidArgument, "project is required", false)
	}
	if path == "" {
		return files.NewError(files.ErrCodeInvalidPath, "path is required", false)
	}
	if containsDotDot(path) {
		return files.NewError(files.ErrCodeInvalidPath, "path must not contain ..", false)
	}
	return nil
}

// containsDotDot returns true when the path includes a parent traversal segment.
func containsDotDot(path string) bool {
	for _, part := range splitPathSegments(path) {
		if part == ".." {
			return true
		}
	}
	return false
}

// splitPathSegments splits on '/' for traversal checks; stdlib path.Split would re-import logic.
func splitPathSegments(path string) []string {
	out := make([]string, 0, 4)
	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || path[i] == '/' {
			if i > start {
				out = append(out, path[start:i])
			}
			start = i + 1
		}
	}
	return out
}
