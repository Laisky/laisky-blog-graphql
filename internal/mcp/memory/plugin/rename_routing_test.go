package plugin

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

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

// TestRenameRoutingForwardsTheWholeRequest verifies that the manager and the
// shadow wrapper pass one rename request, including its target project,
// through unchanged: there is no separate cross-project entry point to miss.
func TestRenameRoutingForwardsTheWholeRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	move := files.RenameRequest{Project: "alpha", FromPath: "/a.md", TargetProject: "beta", ToPath: "/b.md", Overwrite: true}
	same := files.RenameRequest{Project: "alpha", FromPath: "/a.md", ToPath: "/b.md"}

	rag := &renameRecordingPlugin{testPlugin: testPlugin{name: DefaultPluginRAG}}
	pageindex := &renameRecordingPlugin{testPlugin: testPlugin{name: DefaultPluginPageIndex}}
	mgr, err := NewManager(DefaultPluginRAG, rag, pageindex)
	require.NoError(t, err)
	for _, req := range []files.RenameRequest{move, same} {
		result, err := mgr.Rename(ctx, files.AuthContext{}, req)
		require.NoError(t, err)
		require.Equal(t, 1, result.MovedCount)
	}
	_, err = mgr.Rename(WithOverride(ctx, DefaultPluginPageIndex), files.AuthContext{}, move)
	require.NoError(t, err)
	require.Equal(t, []files.RenameRequest{move, same}, rag.recorded())
	require.Equal(t, []files.RenameRequest{move}, pageindex.recorded())

	live := &renameRecordingPlugin{testPlugin: testPlugin{name: "live"}}
	shadow := &renameRecordingPlugin{testPlugin: testPlugin{name: "shadow"}}
	wrap, err := NewShadowPlugin(ShadowConfig{Live: live, Shadow: shadow, Recorder: NewMemRecorder()})
	require.NoError(t, err)
	_, err = wrap.Rename(ctx, files.AuthContext{}, move)
	require.NoError(t, err)
	require.NoError(t, wrap.Stop(ctx), "stop drains the shadow mutation")
	require.Equal(t, []files.RenameRequest{move}, live.recorded())
	require.Equal(t, []files.RenameRequest{move}, shadow.recorded())
}
