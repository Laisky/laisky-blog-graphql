package plugin

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// stubPlugin is a minimal Plugin implementation used to verify Manager wiring
// of a ShadowPlugin: it counts Write calls so the integration test can assert
// the wrapper dual-wrote to both live and shadow.
type stubPlugin struct {
	name       string
	writeCount int32
	startCount int32
	stopCount  int32
	stopErr    error
}

func (s *stubPlugin) Name() string               { return s.name }
func (s *stubPlugin) Capabilities() Capabilities { return Capabilities{} }
func (s *stubPlugin) Start(context.Context) error {
	atomic.AddInt32(&s.startCount, 1)
	return nil
}

func (s *stubPlugin) Stop(context.Context) error {
	atomic.AddInt32(&s.stopCount, 1)
	return s.stopErr
}

func (s *stubPlugin) Stat(context.Context, files.AuthContext, string, string) (files.StatResult, error) {
	return files.StatResult{}, nil
}

func (s *stubPlugin) Read(context.Context, files.AuthContext, string, string, int64, int64) (files.ReadResult, error) {
	return files.ReadResult{}, nil
}

func (s *stubPlugin) List(context.Context, files.AuthContext, string, string, int, int) (files.ListResult, error) {
	return files.ListResult{}, nil
}

func (s *stubPlugin) Write(context.Context, files.AuthContext, string, string, string, string, int64, files.WriteMode) (files.WriteResult, error) {
	atomic.AddInt32(&s.writeCount, 1)
	return files.WriteResult{}, nil
}

func (s *stubPlugin) Delete(context.Context, files.AuthContext, string, string, bool) (files.DeleteResult, error) {
	return files.DeleteResult{}, nil
}

func (s *stubPlugin) Rename(context.Context, files.AuthContext, string, string, string, bool) (files.RenameResult, error) {
	return files.RenameResult{}, nil
}

func (s *stubPlugin) Search(context.Context, files.AuthContext, string, string, string, int) (files.SearchResult, error) {
	return files.SearchResult{}, nil
}

// closeCountingRecorder counts Close() calls so the integration test can
// assert Manager.StopAll triggered ShadowPlugin.Stop, which closes the recorder.
type closeCountingRecorder struct {
	inner    *MemRecorder
	closeCnt int32
}

func newCloseCountingRecorder() *closeCountingRecorder {
	return &closeCountingRecorder{inner: NewMemRecorder()}
}

func (r *closeCountingRecorder) RecordSearch(rec SearchRecord) error {
	return r.inner.RecordSearch(rec)
}

func (r *closeCountingRecorder) RecordMutation(rec MutationRecord) error {
	return r.inner.RecordMutation(rec)
}

func (r *closeCountingRecorder) Close() error {
	atomic.AddInt32(&r.closeCnt, 1)
	return nil
}

// TestShadowManagerIntegrationDualWrite wires a Manager with a ShadowPlugin
// registered under the live plugin's name and verifies:
//   - Manager.Resolve returns the wrapper, transparent to MCP callers.
//   - A Write on the resolved plugin reaches both live and shadow stubs.
//   - Manager.StopAll closes the wrapper's recorder.
func TestShadowManagerIntegrationDualWrite(t *testing.T) {
	t.Parallel()

	live := &stubPlugin{name: "rag"}
	shadow := &stubPlugin{name: "pageindex"}
	rec := newCloseCountingRecorder()

	wrapper, err := NewShadowPlugin(ShadowConfig{
		Name:     "rag",
		Live:     live,
		Shadow:   shadow,
		Recorder: rec,
	})
	require.NoError(t, err)
	require.Equal(t, "rag", wrapper.Name())

	mgr, err := NewManager("rag", wrapper, shadow)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, mgr.StartAll(ctx))

	resolved, err := mgr.Resolve(ctx, files.AuthContext{}, "proj", "")
	require.NoError(t, err)
	require.Equal(t, "rag", resolved.Name())
	// Identity check: the resolved plugin must be the wrapper, not live directly.
	require.Same(t, wrapper, resolved)

	_, err = resolved.Write(ctx, files.AuthContext{}, "proj", "/p", "data", "utf-8", 0, files.WriteMode("overwrite"))
	require.NoError(t, err)

	// Drain shadow inflight via Stop; this also closes the recorder.
	require.NoError(t, mgr.StopAll(ctx))

	require.Equal(t, int32(1), atomic.LoadInt32(&live.writeCount), "live should receive the write")
	require.Equal(t, int32(1), atomic.LoadInt32(&shadow.writeCount), "shadow should receive the dual-write")
	require.Equal(t, int32(1), atomic.LoadInt32(&rec.closeCnt), "recorder Close should fire on StopAll")
}
