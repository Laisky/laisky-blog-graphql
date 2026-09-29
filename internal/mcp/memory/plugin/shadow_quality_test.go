package plugin

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

type qualityContextKey struct{}
type contextShadow struct {
	*shadowFakePlugin
	entered chan context.Context
	release chan struct{}
}

func (s *contextShadow) Write(ctx context.Context, _ files.AuthContext, _, _, _, _ string, _ int64, _ files.WriteMode) (files.WriteResult, error) {
	s.entered <- ctx
	<-s.release
	return files.WriteResult{}, nil
}

// TestShadowPreservesValuesAndNeverClosesActiveRecorder covers cancellation, safe drain and repeated Stop.
func TestShadowPreservesValuesAndNeverClosesActiveRecorder(t *testing.T) {
	shadow := &contextShadow{shadowFakePlugin: newFake("shadow"), entered: make(chan context.Context, 1), release: make(chan struct{})}
	rec := &closeTrackingRecorder{}
	p, err := NewShadowPlugin(ShadowConfig{Live: newFake("live"), Shadow: shadow, Recorder: rec, OpTimeout: time.Second})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), qualityContextKey{}, "tenant-context"))
	_, err = p.Write(ctx, files.AuthContext{}, "p", "f", "v", "utf-8", 0, files.WriteMode("overwrite"))
	require.NoError(t, err)
	var child context.Context
	select {
	case child = <-shadow.entered:
	case <-time.After(time.Second):
		t.Fatal("shadow did not start")
	}
	cancel()
	require.NoError(t, child.Err(), "parent response lifetime must not cancel detached work")
	require.Equal(t, "tenant-context", child.Value(qualityContextKey{}))
	stopCtx, stopCancel := context.WithCancel(t.Context())
	stopCancel()
	require.ErrorIs(t, p.Stop(stopCtx), context.Canceled)
	require.Zero(t, atomic.LoadInt32(&rec.closed), "active callback still owns recorder")
	close(shadow.release)
	require.NoError(t, p.Stop(t.Context()))
	require.NoError(t, p.Stop(t.Context()), "Stop must be idempotent")
	require.EqualValues(t, 1, atomic.LoadInt32(&rec.closed))
}

// TestShadowConcurrentAdmissionAndStop runs the WaitGroup admission boundary under the race detector.
func TestShadowConcurrentAdmissionAndStop(t *testing.T) {
	for n := 0; n < 20; n++ {
		p, _ := newShadowFromFakes(t, newFake("live"), newFake("shadow"))
		var callers sync.WaitGroup
		for i := 0; i < 16; i++ {
			callers.Add(1)
			go func() {
				defer callers.Done()
				_, _ = p.Write(t.Context(), files.AuthContext{}, "p", "f", "v", "utf-8", 0, files.WriteMode("overwrite"))
			}()
		}
		require.NoError(t, p.Stop(t.Context()))
		callers.Wait()
		require.NoError(t, p.Stop(t.Context()))
	}
}
