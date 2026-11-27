package cmd

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/Laisky/zap/zaptest/observer"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
	ragplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/rag"
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

// newObservedLogger returns a logger whose entries the test can inspect.
func newObservedLogger(t *testing.T) (logSDK.Logger, *observer.ObservedLogs) {
	t.Helper()
	core, observed := observer.New(zapcore.DebugLevel)
	logger, err := logSDK.NewWithName("api", logSDK.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
	require.NoError(t, err)
	return logger, observed
}

// TestNewMCPFileManagerIgnoresStalePageIndexConfig verifies that a formerly
// configured PageIndex, with an API key and as the default plugin, passes
// startup validation, registers only rag, routes the default to rag, refuses
// an explicit pageindex selection and never logs the configured key.
func TestNewMCPFileManagerIgnoresStalePageIndexConfig(t *testing.T) {
	const secret = "sk-stale-pageindex-key"
	get := newMapConfigGetter(map[string]any{"settings": map[string]any{"mcp": map[string]any{"tools": map[string]any{"memory": map[string]any{
		"default_plugin": "PageIndex",
		"plugins": map[string]any{"pageindex": map[string]any{
			"llm":     map[string]any{"api_key": secret, "base_url": "https://llm.invalid/v1"},
			"indexer": "corrupt",
		}},
	}}}}})
	require.NoError(t, validateStartupConfigWithGetter(get), "a stale pageindex default must not fail startup")

	rag, err := ragplugin.New(&files.Service{})
	require.NoError(t, err)
	logger, observed := newObservedLogger(t)
	manager, err := newMCPFileManager(get, logger, "PageIndex", rag)
	require.NoError(t, err)
	require.Equal(t, mcpplugin.DefaultPluginRAG, manager.DefaultPlugin())
	require.Equal(t, []string{mcpplugin.DefaultPluginRAG}, manager.AvailablePlugins())

	require.Len(t, observed.FilterLevelExact(zapcore.WarnLevel).All(), 2)
	for _, entry := range observed.All() {
		require.NotContains(t, entry.Message, secret)
		for _, field := range entry.Context {
			require.NotContains(t, field.String, secret)
		}
	}

	resolved, err := manager.Resolve(context.Background(), files.AuthContext{}, "demo", "")
	require.NoError(t, err)
	require.Same(t, rag, resolved)
	_, err = manager.Resolve(context.Background(), files.AuthContext{}, "demo", mcpplugin.DefaultPluginPageIndex)
	resolveErr, ok := mcpplugin.AsResolveError(err)
	require.True(t, ok)
	require.Equal(t, mcpplugin.PageIndexDisabledMessage, resolveErr.Error())
	require.Equal(t, []string{mcpplugin.DefaultPluginRAG}, resolveErr.Available)
}

// TestStaleShadowSettingsNeitherWrapNorReplay verifies that a configuration
// still carrying the removed settings.mcp.tools.memory.shadow.* block passes
// startup validation, logs exactly one warning, and changes nothing else: the
// manager routes to rag itself, every operation runs once (also across Stop,
// where an asynchronous replay would have drained), and no recorder file is
// created.
func TestStaleShadowSettingsNeitherWrapNorReplay(t *testing.T) {
	recorderPath := filepath.Join(t.TempDir(), "shadow.jsonl")
	get := newMapConfigGetter(map[string]any{"settings": map[string]any{"mcp": map[string]any{"tools": map[string]any{"memory": map[string]any{
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
	}}}}})
	require.NoError(t, validateStartupConfigWithGetter(get), "stale shadow keys must not fail startup")

	logger, observed := newObservedLogger(t)
	rag := &callCountingPlugin{name: mcpplugin.DefaultPluginRAG}
	manager, err := newMCPFileManager(get, logger, mcpplugin.DefaultPluginRAG, rag)
	require.NoError(t, err)

	entries := observed.All()
	require.Len(t, entries, 1, "the stale setting produces exactly one log entry")
	require.Equal(t, zapcore.WarnLevel, entries[0].Level)
	require.Contains(t, entries[0].Message, "shadow replay was removed")
	require.Equal(t, removedShadowReplayEnabledKey, entries[0].ContextMap()["setting"])

	ctx := context.Background()
	resolved, err := manager.Resolve(ctx, files.AuthContext{}, "proj", "")
	require.NoError(t, err)
	require.Same(t, rag, resolved, "rag is registered unwrapped")
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
	require.NoFileExists(t, recorderPath, "no shadow recorder is opened")
	require.Len(t, observed.All(), 1, "routing logs nothing more")
}

// TestNewMCPFileManagerIsSilentForOrdinaryConfig verifies an ordinary RAG
// configuration, and an absent, disabled or malformed shadow toggle, register
// rag silently with the configured default.
func TestNewMCPFileManagerIsSilentForOrdinaryConfig(t *testing.T) {
	for name, cfg := range map[string]map[string]any{
		"absent":   {"settings": map[string]any{"mcp": map[string]any{}}},
		"disabled": {"settings": map[string]any{"mcp": map[string]any{"tools": map[string]any{"memory": map[string]any{"shadow": map[string]any{"enabled": false}}}}}},
		"invalid":  {"settings": map[string]any{"mcp": map[string]any{"tools": map[string]any{"memory": map[string]any{"shadow": map[string]any{"enabled": "maybe"}}}}}},
	} {
		t.Run(name, func(t *testing.T) {
			logger, observed := newObservedLogger(t)
			manager, err := newMCPFileManager(newMapConfigGetter(cfg), logger, "", &callCountingPlugin{name: mcpplugin.DefaultPluginRAG})
			require.NoError(t, err)
			require.Equal(t, mcpplugin.DefaultPluginRAG, manager.DefaultPlugin())
			require.Zero(t, observed.Len())
		})
	}
}
