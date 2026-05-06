package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// shadowFakePlugin is a configurable plugin stub for ShadowPlugin tests.
type shadowFakePlugin struct {
	name    string
	caps    Capabilities
	startCh chan struct{}
	stopCh  chan struct{}

	readResp  files.ReadResult
	statResp  files.StatResult
	listResp  files.ListResult
	writeRes  files.WriteResult
	delRes    files.DeleteResult
	rnRes     files.RenameResult
	searchRes files.SearchResult

	writeErr  error
	delErr    error
	rnErr     error
	searchErr error

	writeCount  int32
	deleteCount int32
	renameCount int32
	searchCount int32

	searchDelay time.Duration

	startErr error
	stopErr  error
}

func newFake(name string) *shadowFakePlugin {
	return &shadowFakePlugin{name: name, startCh: make(chan struct{}, 1), stopCh: make(chan struct{}, 1)}
}

func (p *shadowFakePlugin) Name() string                                                                                                                                                  { return p.name }
func (p *shadowFakePlugin) Capabilities() Capabilities                                                                                                                                    { return p.caps }
func (p *shadowFakePlugin) Start(context.Context) error                                                                                                                                   { p.startCh <- struct{}{}; return p.startErr }
func (p *shadowFakePlugin) Stop(context.Context) error                                                                                                                                    { p.stopCh <- struct{}{}; return p.stopErr }
func (p *shadowFakePlugin) Stat(context.Context, files.AuthContext, string, string) (files.StatResult, error)                                                                            { return p.statResp, nil }
func (p *shadowFakePlugin) Read(context.Context, files.AuthContext, string, string, int64, int64) (files.ReadResult, error)                                                              { return p.readResp, nil }
func (p *shadowFakePlugin) List(context.Context, files.AuthContext, string, string, int, int) (files.ListResult, error)                                                                  { return p.listResp, nil }
func (p *shadowFakePlugin) Write(context.Context, files.AuthContext, string, string, string, string, int64, files.WriteMode) (files.WriteResult, error) {
	atomic.AddInt32(&p.writeCount, 1)
	return p.writeRes, p.writeErr
}
func (p *shadowFakePlugin) Delete(context.Context, files.AuthContext, string, string, bool) (files.DeleteResult, error) {
	atomic.AddInt32(&p.deleteCount, 1)
	return p.delRes, p.delErr
}
func (p *shadowFakePlugin) Rename(context.Context, files.AuthContext, string, string, string, bool) (files.RenameResult, error) {
	atomic.AddInt32(&p.renameCount, 1)
	return p.rnRes, p.rnErr
}
func (p *shadowFakePlugin) Search(ctx context.Context, _ files.AuthContext, _ string, _ string, _ string, _ int) (files.SearchResult, error) {
	atomic.AddInt32(&p.searchCount, 1)
	if p.searchDelay > 0 {
		select {
		case <-time.After(p.searchDelay):
		case <-ctx.Done():
			return files.SearchResult{}, ctx.Err()
		}
	}
	return p.searchRes, p.searchErr
}

func newShadowFromFakes(t *testing.T, live, shadow *shadowFakePlugin) (*ShadowPlugin, *MemRecorder) {
	t.Helper()
	mem := NewMemRecorder()
	wrap, err := NewShadowPlugin(ShadowConfig{Live: live, Shadow: shadow, Recorder: mem})
	require.NoError(t, err)
	return wrap, mem
}

// TestShadowPluginForwardsLiveOnRead checks reads return live's bytes.
func TestShadowPluginForwardsLiveOnRead(t *testing.T) {
	t.Parallel()

	live := newFake("live")
	live.readResp = files.ReadResult{Content: "live-bytes"}
	live.statResp = files.StatResult{Exists: true, Size: 7}
	live.listResp = files.ListResult{Entries: []files.FileEntry{{Path: "/a"}}}

	shadow := newFake("shadow")
	shadow.readResp = files.ReadResult{Content: "shadow-bytes"}
	shadow.statResp = files.StatResult{Exists: false}

	wrap, _ := newShadowFromFakes(t, live, shadow)
	ctx := context.Background()

	got, err := wrap.Read(ctx, files.AuthContext{}, "p", "/a", 0, 16)
	require.NoError(t, err)
	require.Equal(t, "live-bytes", got.Content)

	stat, err := wrap.Stat(ctx, files.AuthContext{}, "p", "/a")
	require.NoError(t, err)
	require.True(t, stat.Exists)

	list, err := wrap.List(ctx, files.AuthContext{}, "p", "/", 1, 10)
	require.NoError(t, err)
	require.Len(t, list.Entries, 1)
}

// TestShadowPluginDualWritesMutation checks both live and shadow see writes
// and that a shadow failure is recorded but does not propagate.
func TestShadowPluginDualWritesMutation(t *testing.T) {
	t.Parallel()

	live := newFake("live")
	shadow := newFake("shadow")
	shadow.writeErr = errors.New("shadow boom")

	wrap, mem := newShadowFromFakes(t, live, shadow)
	ctx := context.Background()

	_, err := wrap.Write(ctx, files.AuthContext{}, "proj", "/x", "hi", "utf8", 0, files.WriteModeOverwrite)
	require.NoError(t, err)

	require.NoError(t, wrap.Stop(ctx))
	require.Equal(t, int32(1), atomic.LoadInt32(&live.writeCount))
	require.Equal(t, int32(1), atomic.LoadInt32(&shadow.writeCount))

	_, mutations := mem.Records()
	require.Len(t, mutations, 1)
	require.Equal(t, "write", mutations[0].Op)
	require.Equal(t, "shadow boom", mutations[0].ShadowErr)
	require.Empty(t, mutations[0].LiveErr)
}

// TestShadowPluginLiveErrorBlocksShadow checks that on a live failure the
// shadow plugin is not called — we must never let the shadow drift ahead of a
// failed live mutation.
func TestShadowPluginLiveErrorBlocksShadow(t *testing.T) {
	t.Parallel()

	live := newFake("live")
	live.writeErr = errors.New("live no")
	shadow := newFake("shadow")

	wrap, _ := newShadowFromFakes(t, live, shadow)
	ctx := context.Background()
	_, err := wrap.Write(ctx, files.AuthContext{}, "proj", "/x", "hi", "utf8", 0, files.WriteModeOverwrite)
	require.Error(t, err)
	require.NoError(t, wrap.Stop(ctx))
	require.Equal(t, int32(0), atomic.LoadInt32(&shadow.writeCount))
}

// TestShadowPluginShutdownDrainsInFlight asserts Stop honours the grace period
// for in-flight shadow searches.
func TestShadowPluginShutdownDrainsInFlight(t *testing.T) {
	t.Parallel()

	live := newFake("live")
	shadow := newFake("shadow")
	shadow.searchDelay = 200 * time.Millisecond

	mem := NewMemRecorder()
	wrap, err := NewShadowPlugin(ShadowConfig{
		Live:       live,
		Shadow:     shadow,
		Recorder:   mem,
		DrainGrace: 1 * time.Second,
		OpTimeout:  1 * time.Second,
	})
	require.NoError(t, err)

	_, err = wrap.Search(context.Background(), files.AuthContext{}, "proj", "q", "/", 5)
	require.NoError(t, err)

	start := time.Now()
	require.NoError(t, wrap.Stop(context.Background()))
	require.GreaterOrEqual(t, time.Since(start), 100*time.Millisecond)

	searches, _ := mem.Records()
	require.Len(t, searches, 1)
	require.Equal(t, "live", searches[0].LivePlugin)
	require.Equal(t, "shadow", searches[0].ShadowPlugin)
}

// TestShadowPluginStartLogsShadowFailure confirms a shadow Start failure does
// not block the wrapper; the live plugin must still come up.
func TestShadowPluginStartLogsShadowFailure(t *testing.T) {
	t.Parallel()

	live := newFake("live")
	shadow := newFake("shadow")
	shadow.startErr = errors.New("shadow start nope")

	wrap, _ := newShadowFromFakes(t, live, shadow)
	require.NoError(t, wrap.Start(context.Background()))
	require.NoError(t, wrap.Stop(context.Background()))
}

// stubJudge always returns the configured Winner.
type stubJudge struct {
	winner string
}

func (s stubJudge) CompareResults(_ context.Context, _ string, _, _ SearchResult) (Verdict, error) {
	return Verdict{Winner: s.winner}, nil
}

// pickByContentJudge picks "A" if the result has more chunks, "B" otherwise,
// "TIE" if equal. The shadow-replay scorer position-swaps inputs internally,
// but inverts back before counting, so the sign of the decision is stable.
type pickByContentJudge struct{}

func (pickByContentJudge) CompareResults(_ context.Context, _ string, a, b SearchResult) (Verdict, error) {
	switch {
	case len(a.Chunks) > len(b.Chunks):
		return Verdict{Winner: "A", Reason: "more chunks"}, nil
	case len(b.Chunks) > len(a.Chunks):
		return Verdict{Winner: "B", Reason: "more chunks"}, nil
	default:
		return Verdict{Winner: "TIE"}, nil
	}
}

func makeShadowReplayRecord(query string, liveChunks, shadowChunks int) SearchRecord {
	mk := func(n int) files.SearchResult {
		out := files.SearchResult{}
		for i := 0; i < n; i++ {
			out.Chunks = append(out.Chunks, files.ChunkEntry{FilePath: "/p", ChunkContent: query})
		}
		return out
	}
	return SearchRecord{
		Timestamp:    time.Now(),
		Project:      "p",
		Query:        query,
		LiveResult:   mk(liveChunks),
		ShadowResult: mk(shadowChunks),
		LivePlugin:   "live",
		ShadowPlugin: "shadow",
	}
}

// TestScoreShadowReplay_AClearWinner — every record favors live; B win-rate < 0.45.
// §7.8 says: below 45% → investigate.
func TestScoreShadowReplay_AClearWinner(t *testing.T) {
	t.Parallel()

	records := make([]SearchRecord, 60)
	for i := range records {
		records[i] = makeShadowReplayRecord("q", 5, 1)
	}

	res, err := ScoreShadowReplay(context.Background(), records, pickByContentJudge{}, ScoreOpts{})
	require.NoError(t, err)
	require.Equal(t, 60, res.NQueries)
	require.Equal(t, 60, res.AWins)
	require.Less(t, res.PValue, 0.05)
	require.Less(t, res.BWinRate, 0.45)
	require.Equal(t, DecisionInvestigate, res.PromotionDecision)
}

// TestScoreShadowReplay_BClearWinner — every record favors shadow; promote.
func TestScoreShadowReplay_BClearWinner(t *testing.T) {
	t.Parallel()

	records := make([]SearchRecord, 60)
	for i := range records {
		records[i] = makeShadowReplayRecord("q", 1, 5)
	}

	res, err := ScoreShadowReplay(context.Background(), records, pickByContentJudge{}, ScoreOpts{})
	require.NoError(t, err)
	require.Equal(t, 60, res.NQueries)
	require.Equal(t, 60, res.BWins)
	require.GreaterOrEqual(t, res.BWinRate, 0.55)
	require.Less(t, res.PValue, 0.05)
	require.Equal(t, DecisionPromoteB, res.PromotionDecision)
}

// TestScoreShadowReplay_Inconclusive — ties land in [0.45, 0.55] → stay_A.
func TestScoreShadowReplay_Inconclusive(t *testing.T) {
	t.Parallel()

	records := make([]SearchRecord, 60)
	for i := range records {
		records[i] = makeShadowReplayRecord("q", 2, 2)
	}

	res, err := ScoreShadowReplay(context.Background(), records, pickByContentJudge{}, ScoreOpts{})
	require.NoError(t, err)
	require.Equal(t, 60, res.Ties)
	require.InDelta(t, 0.5, res.BWinRate, 1e-9)
	require.Equal(t, DecisionStayA, res.PromotionDecision)
}

// TestScoreShadowReplay_BelowMinQueries — small samples never promote.
func TestScoreShadowReplay_BelowMinQueries(t *testing.T) {
	t.Parallel()

	records := make([]SearchRecord, 5)
	for i := range records {
		records[i] = makeShadowReplayRecord("q", 1, 5)
	}
	res, err := ScoreShadowReplay(context.Background(), records, pickByContentJudge{}, ScoreOpts{})
	require.NoError(t, err)
	require.Equal(t, DecisionStayA, res.PromotionDecision)
}

// TestJSONLRecorder_RoundTrip writes one search and one mutation record, then
// reads the file back and asserts byte-for-byte equality on key fields.
func TestJSONLRecorder_RoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "shadow.jsonl")
	rec, err := NewJSONLRecorder(path)
	require.NoError(t, err)

	search := SearchRecord{
		Timestamp:      time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC),
		Project:        "p",
		Query:          "what is x",
		PathPrefix:     "/docs",
		Limit:          5,
		LiveResult:     files.SearchResult{Chunks: []files.ChunkEntry{{FilePath: "/a", Score: 0.9}}},
		ShadowResult:   files.SearchResult{Chunks: []files.ChunkEntry{{FilePath: "/b", Score: 0.7}}},
		LiveDuration:   42 * time.Millisecond,
		ShadowDuration: 51 * time.Millisecond,
		LivePlugin:     "live",
		ShadowPlugin:   "shadow",
	}
	require.NoError(t, rec.RecordSearch(search))

	mutation := MutationRecord{
		Timestamp: time.Date(2026, 5, 6, 12, 0, 1, 0, time.UTC),
		Op:        "write",
		Project:   "p",
		Path:      "/a",
		ShadowErr: "boom",
	}
	require.NoError(t, rec.RecordMutation(mutation))
	require.NoError(t, rec.Close())

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	scanner := bufio.NewScanner(f)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	require.NoError(t, scanner.Err())
	require.Len(t, lines, 2)

	var first jsonlEnvelope
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.Equal(t, "search", first.Kind)
	require.NotNil(t, first.Search)
	require.Equal(t, "what is x", first.Search.Query)
	require.Equal(t, "/docs", first.Search.PathPrefix)
	require.Equal(t, 5, first.Search.Limit)
	require.Len(t, first.Search.LiveResult.Chunks, 1)
	require.Equal(t, "/a", first.Search.LiveResult.Chunks[0].FilePath)

	var second jsonlEnvelope
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &second))
	require.Equal(t, "mutation", second.Kind)
	require.NotNil(t, second.Mutation)
	require.Equal(t, "write", second.Mutation.Op)
	require.Equal(t, "boom", second.Mutation.ShadowErr)

	require.Error(t, rec.Rotate())
}

// TestMultiRecorderFanOut confirms NewMultiRecorder dispatches to all kids.
func TestMultiRecorderFanOut(t *testing.T) {
	t.Parallel()

	a := NewMemRecorder()
	b := NewMemRecorder()
	multi := NewMultiRecorder(a, b, nil)

	require.NoError(t, multi.RecordSearch(SearchRecord{Project: "p"}))
	require.NoError(t, multi.RecordMutation(MutationRecord{Op: "write"}))
	require.NoError(t, multi.Close())

	sa, ma := a.Records()
	sb, mb := b.Records()
	require.Len(t, sa, 1)
	require.Len(t, sb, 1)
	require.Len(t, ma, 1)
	require.Len(t, mb, 1)
}
