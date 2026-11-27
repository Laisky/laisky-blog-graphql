package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/Laisky/zap/zaptest/observer"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// callCountingPlugin records every operation that reaches it, in order.
type callCountingPlugin struct {
	name  string
	mu    sync.Mutex
	calls []string
}

// record appends one operation name.
func (p *callCountingPlugin) record(op string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, op)
}

// recorded returns a copy of the recorded operation names.
func (p *callCountingPlugin) recorded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

// Name returns the plugin name.
func (p *callCountingPlugin) Name() string { return p.name }

// Capabilities returns zero-value capabilities.
func (p *callCountingPlugin) Capabilities() mcpplugin.Capabilities { return mcpplugin.Capabilities{} }

// Start records the lifecycle call.
func (p *callCountingPlugin) Start(context.Context) error { p.record("start"); return nil }

// Stop records the lifecycle call.
func (p *callCountingPlugin) Stop(context.Context) error { p.record("stop"); return nil }

// Stat records the call.
func (p *callCountingPlugin) Stat(context.Context, files.AuthContext, string, string) (files.StatResult, error) {
	p.record("stat")
	return files.StatResult{}, nil
}

// Read records the call.
func (p *callCountingPlugin) Read(context.Context, files.AuthContext, string, string, int64, int64) (files.ReadResult, error) {
	p.record("read")
	return files.ReadResult{}, nil
}

// Write records the call.
func (p *callCountingPlugin) Write(context.Context, files.AuthContext, string, string, string, string, int64, files.WriteMode) (files.WriteResult, error) {
	p.record("write")
	return files.WriteResult{}, nil
}

// Delete records the call.
func (p *callCountingPlugin) Delete(context.Context, files.AuthContext, string, string, bool) (files.DeleteResult, error) {
	p.record("delete")
	return files.DeleteResult{}, nil
}

// Rename records the call.
func (p *callCountingPlugin) Rename(context.Context, files.AuthContext, files.RenameRequest) (files.RenameResult, error) {
	p.record("rename")
	return files.RenameResult{}, nil
}

// List records the call.
func (p *callCountingPlugin) List(context.Context, files.AuthContext, string, string, int, int) (files.ListResult, error) {
	p.record("list")
	return files.ListResult{}, nil
}

// Search records the call.
func (p *callCountingPlugin) Search(context.Context, files.AuthContext, string, string, string, int) (files.SearchResult, error) {
	p.record("search")
	return files.SearchResult{}, nil
}

// TestStaleShadowSettingsNeitherWrapNorReplay verifies that a configuration
// still carrying the removed settings.mcp.tools.memory.shadow.* block passes
// startup validation, logs exactly one warning, and changes nothing else: the
// manager routes to the registered plugins themselves, every operation runs
// once on the selected plugin, the other plugin never receives a replay (not
// even after Stop, which is where an asynchronous replay would have drained),
// and no recorder file is created.
func TestStaleShadowSettingsNeitherWrapNorReplay(t *testing.T) {
	recorderPath := filepath.Join(t.TempDir(), "shadow.jsonl")
	stale := map[string]any{"settings": map[string]any{"mcp": map[string]any{"tools": map[string]any{"memory": map[string]any{
		"default_plugin": "rag",
		"shadow": map[string]any{
			"enabled":       true,
			"live_plugin":   "rag",
			"shadow_plugin": "pageindex",
			"recorder_path": recorderPath,
			"concurrency":   "not-a-number",
			"op_timeout":    "not-a-duration",
			"drain_grace":   -1,
		},
	}}}}}
	get := newMapConfigGetter(stale)
	require.NoError(t, validateStartupConfigWithGetter(get), "stale shadow keys must not fail startup")

	core, observed := observer.New(zapcore.DebugLevel)
	logger, err := logSDK.NewWithName("api", logSDK.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
	require.NoError(t, err)

	rag := &callCountingPlugin{name: mcpplugin.DefaultPluginRAG}
	pageindex := &callCountingPlugin{name: mcpplugin.DefaultPluginPageIndex}
	manager, err := newMCPFileManager(get, logger, mcpplugin.DefaultPluginRAG, rag, pageindex)
	require.NoError(t, err)

	entries := observed.All()
	require.Len(t, entries, 1, "the stale setting produces exactly one log entry")
	require.Equal(t, zapcore.WarnLevel, entries[0].Level)
	require.Contains(t, entries[0].Message, "shadow replay was removed")
	require.Contains(t, entries[0].Message, "ignored")
	require.Equal(t, removedShadowReplayEnabledKey, entries[0].ContextMap()["setting"])

	ctx := context.Background()
	resolved, err := manager.Resolve(ctx, files.AuthContext{}, "proj", "")
	require.NoError(t, err)
	require.Same(t, rag, resolved, "the default plugin is registered unwrapped")
	resolved, err = manager.ForName(mcpplugin.DefaultPluginPageIndex)
	require.NoError(t, err)
	require.Same(t, pageindex, resolved, "the former shadow target is registered unwrapped")

	require.NoError(t, manager.StartAll(ctx))
	_, err = manager.Write(ctx, files.AuthContext{}, "proj", "/a.md", "x", "utf-8", 0, files.WriteModeAppend)
	require.NoError(t, err)
	_, err = manager.Rename(ctx, files.AuthContext{}, files.RenameRequest{Project: "proj", FromPath: "/a.md", TargetProject: "proj", ToPath: "/b.md"})
	require.NoError(t, err)
	_, err = manager.Delete(ctx, files.AuthContext{}, "proj", "/b.md", false)
	require.NoError(t, err)
	_, err = manager.Search(ctx, files.AuthContext{}, "proj", "query", "", 5)
	require.NoError(t, err)
	require.NoError(t, manager.StopAll(ctx))

	require.Equal(t, []string{"start", "write", "rename", "delete", "search", "stop"}, rag.recorded())
	require.Equal(t, []string{"start", "stop"}, pageindex.recorded(), "no operation is replayed on another plugin")
	require.NoFileExists(t, recorderPath, "no shadow recorder is opened")
	require.Len(t, observed.All(), 1, "routing logs nothing about shadow replay")
}

// TestNewMCPFileManagerWarnsOnlyForEnabledShadow verifies that an absent or
// disabled shadow toggle registers the plugins silently.
func TestNewMCPFileManagerWarnsOnlyForEnabledShadow(t *testing.T) {
	for name, cfg := range map[string]map[string]any{
		"absent":   {},
		"disabled": {"settings": map[string]any{"mcp": map[string]any{"tools": map[string]any{"memory": map[string]any{"shadow": map[string]any{"enabled": false}}}}}},
		"invalid":  {"settings": map[string]any{"mcp": map[string]any{"tools": map[string]any{"memory": map[string]any{"shadow": map[string]any{"enabled": "maybe"}}}}}},
	} {
		t.Run(name, func(t *testing.T) {
			core, observed := observer.New(zapcore.DebugLevel)
			logger, err := logSDK.NewWithName("api", logSDK.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
			require.NoError(t, err)
			rag := &callCountingPlugin{name: mcpplugin.DefaultPluginRAG}
			manager, err := newMCPFileManager(newMapConfigGetter(cfg), logger, "", rag)
			require.NoError(t, err)
			require.Equal(t, mcpplugin.DefaultPluginRAG, manager.DefaultPlugin())
			for _, entry := range observed.All() {
				require.False(t, strings.Contains(entry.Message, "shadow"), entry.Message)
			}
		})
	}
}
