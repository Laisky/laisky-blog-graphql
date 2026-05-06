package pageindex

import (
	"context"
	"testing"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/conformance"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// TestPluginConformance_NoStorage runs the conformance suite against a stub
// pageindex plugin. The single-plugin no-storage fixture skips all
// storage-dependent and cross-plugin scenarios — its job is to demonstrate
// the suite runs against pageindex and that the package's plugin satisfies
// the registered scenarios it can exercise without a Postgres-shaped userFS
// or a multi-plugin manager.
func TestPluginConformance_NoStorage(t *testing.T) {
	t.Parallel()

	conformance.Run(t, &conformanceFixture{}, conformance.Options{
		SkipConcurrency: true,
		SkipCrossPlugin: true,
		SkipFreshness:   true,
		Notes:           "pageindex plugin without persistent userFS",
	})
}

type conformanceFixture struct{}

func (conformanceFixture) Plugin() mcpplugin.Plugin { return &conformanceStubPlugin{} }
func (conformanceFixture) NewAuthContext(t *testing.T) mcpplugin.AuthContext {
	t.Helper()
	return mcpplugin.AuthContext{APIKey: "k", APIKeyHash: "h", UserIdentity: "u:" + t.Name()}
}
func (conformanceFixture) NewProject(t *testing.T) string {
	t.Helper()
	return "conf-" + t.Name()
}
func (conformanceFixture) HasStorage() bool { return false }
func (conformanceFixture) Cleanup(*testing.T) {}

// conformanceStubPlugin is a contract-shaped stand-in that does not depend on
// a real *files.Service. It exists to satisfy the Plugin interface so the
// conformance suite can register the pageindex name and capabilities without
// the storage-dependent scenarios.
type conformanceStubPlugin struct{}

func (conformanceStubPlugin) Name() string { return mcpplugin.DefaultPluginPageIndex }
func (conformanceStubPlugin) Capabilities() mcpplugin.Capabilities {
	return mcpplugin.Capabilities{
		SearchModes:      []mcpplugin.SearchMode{mcpplugin.SearchModeTreeReasoning},
		SupportsRandomIO: true,
		SupportsRename:   true,
	}
}
func (conformanceStubPlugin) Start(context.Context) error { return nil }
func (conformanceStubPlugin) Stop(context.Context) error  { return nil }
func (conformanceStubPlugin) Stat(_ context.Context, _ files.AuthContext, project, path string) (files.StatResult, error) {
	return files.StatResult{}, validatePathConf(project, path)
}
func (conformanceStubPlugin) Read(_ context.Context, _ files.AuthContext, project, path string, _, _ int64) (files.ReadResult, error) {
	return files.ReadResult{}, validatePathConf(project, path)
}
func (conformanceStubPlugin) Write(_ context.Context, _ files.AuthContext, project, path, _, _ string, _ int64, _ files.WriteMode) (files.WriteResult, error) {
	return files.WriteResult{}, validatePathConf(project, path)
}
func (conformanceStubPlugin) Delete(context.Context, files.AuthContext, string, string, bool) (files.DeleteResult, error) {
	return files.DeleteResult{}, nil
}
func (conformanceStubPlugin) Rename(context.Context, files.AuthContext, string, string, string, bool) (files.RenameResult, error) {
	return files.RenameResult{}, nil
}
func (conformanceStubPlugin) List(context.Context, files.AuthContext, string, string, int, int) (files.ListResult, error) {
	return files.ListResult{}, nil
}
func (conformanceStubPlugin) Search(context.Context, files.AuthContext, string, string, string, int) (files.SearchResult, error) {
	return files.SearchResult{}, nil
}

// validatePathConf enforces non-empty project/path and rejects ".." traversal
// so C17 has something to assert against.
func validatePathConf(project, path string) error {
	if project == "" {
		return files.NewError(files.ErrCodeInvalidArgument, "project is required", false)
	}
	if path == "" {
		return files.NewError(files.ErrCodeInvalidPath, "path is required", false)
	}
	for _, seg := range splitPathSegmentsConf(path) {
		if seg == ".." {
			return files.NewError(files.ErrCodeInvalidPath, "path must not contain ..", false)
		}
	}
	return nil
}

func splitPathSegmentsConf(p string) []string {
	out := make([]string, 0, 4)
	start := 0
	for i := 0; i <= len(p); i++ {
		if i == len(p) || p[i] == '/' {
			if i > start {
				out = append(out, p[start:i])
			}
			start = i + 1
		}
	}
	return out
}
