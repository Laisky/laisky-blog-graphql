package pageindex

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

var renameReviewAuth = files.AuthContext{APIKey: "test-key", APIKeyHash: "tenant-a", UserIdentity: "user:test"}

// TestPageIndexRenameIdentityFollowsCatalog verifies rewrites and reuse of an
// old path do not invalidate a different live document.
func TestPageIndexRenameIdentityFollowsCatalog(t *testing.T) {
	db, _, plugin := newCrossProjectPlugin(t)
	ctx := context.Background()
	auth := renameReviewAuth
	write := func(project, path, body string) {
		t.Helper()
		_, err := plugin.Write(ctx, auth, project, path, "# Document\n\n"+body, "utf-8", 0, files.WriteModeTruncate)
		require.NoError(t, err)
	}
	check := func(path, text string) {
		t.Helper()
		result, err := plugin.Search(ctx, auth, "beta", text, path, 10)
		require.NoError(t, err)
		require.NotEmpty(t, result.Chunks)
		for _, chunk := range result.Chunks {
			require.Equal(t, path, chunk.FilePath)
			require.Contains(t, chunk.ChunkContent, text)
		}
	}
	write("beta", "/x.md", "nebula")
	_, err := plugin.Rename(ctx, auth, "beta", "/x.md", "/other.md", false)
	require.NoError(t, err)
	write("alpha", "/incoming.md", "quasar")
	_, err = plugin.RenameAcrossProjects(ctx, auth, "alpha", "/incoming.md", "beta", "/x.md", false)
	require.NoError(t, err)
	storage := PageIndexStorageProject(auth, "beta")
	before, err := plugin.store.GetIndex(ctx, storage)
	require.NoError(t, err)
	require.NotEqual(t, before["/x.md"].DocID, before["/other.md"].DocID)
	write("beta", "/x.md", "pulsar")
	check("/other.md", "nebula")
	check("/x.md", "pulsar")
	after, err := plugin.store.GetIndex(ctx, storage)
	require.NoError(t, err)
	require.Equal(t, before["/x.md"].DocID, after["/x.md"].DocID)
	require.Equal(t, 4, countSystemRows(t, db, storage))

	// A plain write at a vacated path must also get an independent identity.
	_, err = plugin.Rename(ctx, auth, "beta", "/other.md", "/third.md", false)
	require.NoError(t, err)
	write("beta", "/other.md", "andromeda")
	check("/third.md", "nebula")
	check("/other.md", "andromeda")
	_, err = plugin.Delete(ctx, auth, "beta", "/x.md", false)
	require.NoError(t, err)
	check("/third.md", "nebula")
	check("/other.md", "andromeda")
	require.Equal(t, 4, countSystemRows(t, db, storage))
}

// waitingRenameLLM pauses initial publication outside all project locks.
type waitingRenameLLM struct {
	LLM
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (w *waitingRenameLLM) Respond(ctx context.Context, req Request) (*Response, error) {
	first := false
	w.once.Do(func() {
		first = true
		close(w.started)
		select {
		case <-w.release:
		case <-ctx.Done():
		}
	})
	if first {
		return &Response{Text: "The source document describes the quasar signal."}, nil
	}
	return w.LLM.Respond(ctx, req)
}

// TestPageIndexRenameDuringPublication verifies the move rolls back, then can
// be retried after the original publisher finishes without another write.
func TestPageIndexRenameDuringPublication(t *testing.T) {
	llm := &waitingRenameLLM{LLM: NewStubLLM(), started: make(chan struct{}), release: make(chan struct{})}
	_, service, plugin := newCrossProjectPlugin(t, llm)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var release sync.Once
	defer release.Do(func() { close(llm.release) })
	auth := renameReviewAuth
	writeDone := make(chan error, 1)
	go func() {
		_, err := plugin.Write(ctx, auth, "alpha", "/source.md", "# Source\n\nSignal quasar.", "utf-8", 0, files.WriteModeTruncate)
		writeDone <- err
	}()
	select {
	case <-llm.started:
	case <-ctx.Done():
		t.Fatal("PageIndex did not begin tree generation")
	}
	_, err := plugin.RenameAcrossProjects(ctx, auth, "alpha", "/source.md", "beta", "/target.md", false)
	require.True(t, files.IsCode(err, files.ErrCodeResourceBusy), "%v", err)
	_, err = service.Read(ctx, auth, "alpha", "/source.md", 0, -1)
	require.NoError(t, err)
	_, err = service.Read(ctx, auth, "beta", "/target.md", 0, -1)
	require.True(t, files.IsCode(err, files.ErrCodeNotFound), "%v", err)
	release.Do(func() { close(llm.release) })
	select {
	case err = <-writeDone:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("PageIndex did not complete tree generation")
	}
	_, err = plugin.RenameAcrossProjects(ctx, auth, "alpha", "/source.md", "beta", "/target.md", false)
	require.NoError(t, err)
	result, err := plugin.Search(ctx, auth, "beta", "quasar", "/target.md", 10)
	require.NoError(t, err)
	require.NotEmpty(t, result.Chunks)
	require.Contains(t, result.Chunks[0].ChunkContent, "quasar")
}

func TestPageIndexRenameRejectsStaleTreeAndDropsNonIndexableState(t *testing.T) {
	db, service, plugin := newCrossProjectPlugin(t)
	ctx := context.Background()
	auth := renameReviewAuth
	_, err := plugin.Write(ctx, auth, "alpha", "/source.md", "# Source\n\nSignal quasar.", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	_, err = plugin.Write(ctx, auth, "beta", "/target.md", "# Target\n\nSignal nebula.", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	alpha := PageIndexStorageProject(auth, "alpha")
	beta := PageIndexStorageProject(auth, "beta")
	index, err := plugin.store.GetIndex(ctx, alpha)
	require.NoError(t, err)
	id := index["/source.md"].DocID
	tree, err := plugin.store.GetTree(ctx, alpha, id)
	require.NoError(t, err)
	tree.SourceContentHash = files.HashFileContent([]byte("old data"))
	require.NoError(t, plugin.store.PutTree(ctx, alpha, id, tree))
	stale, err := plugin.Search(ctx, auth, "alpha", "quasar", "/source.md", 10)
	require.NoError(t, err)
	require.Empty(t, stale.Chunks)
	_, err = plugin.RenameAcrossProjects(ctx, auth, "alpha", "/source.md", "beta", "/target.md", true)
	require.True(t, files.IsCode(err, files.ErrCodeResourceBusy), "%v", err)
	old, err := service.Read(ctx, auth, "beta", "/target.md", 0, -1)
	require.NoError(t, err)
	require.Contains(t, old.Content, "nebula")

	require.NoError(t, plugin.store.DeleteTree(ctx, alpha, id))
	_, err = plugin.RenameAcrossProjects(ctx, auth, "alpha", "/source.md", "beta", "/target.md", true)
	require.True(t, files.IsCode(err, files.ErrCodeResourceBusy), "%v", err)
	target, err := plugin.Search(ctx, auth, "beta", "nebula", "/target.md", 10)
	require.NoError(t, err)
	require.NotEmpty(t, target.Chunks, "missing source tree must not remove the overwritten target")
	require.Contains(t, target.Chunks[0].ChunkContent, "nebula")

	_, err = plugin.RenameAcrossProjects(ctx, auth, "alpha", "/source.md", "beta", "/target.txt", false)
	require.NoError(t, err, "non-indexable destinations do not require a current tree")
	require.Equal(t, 2, countSystemRows(t, db, alpha))
	require.Equal(t, 3, countSystemRows(t, db, beta), "an unrelated target tree survives")
	_, err = plugin.Rename(ctx, auth, "beta", "/target.txt", "/new.md", false)
	require.True(t, files.IsCode(err, files.ErrCodeInvalidArgument), "%v", err)
}

// ordinaryAtomicFS exposes exactly the pre-existing atomic method set.
type ordinaryAtomicFS struct {
	files.AtomicSystemFS
	renames int
}

// RenameWithState records the legacy transactional routing.
func (s *ordinaryAtomicFS) RenameWithState(ctx context.Context, auth files.AuthContext, project, systemProject, from, to string,
	overwrite bool, mutate files.SystemStateMutator,
) (files.RenameResult, error) {
	s.renames++
	return s.AtomicSystemFS.RenameWithState(ctx, auth, project, systemProject, from, to, overwrite, mutate)
}

func TestPageIndexLegacyAtomicSystemFSStillRenamesAtomically(t *testing.T) {
	_, service, plugin := newCrossProjectPlugin(t)
	legacy := &ordinaryAtomicFS{AtomicSystemFS: plugin.sysFS.(files.AtomicSystemFS)}
	plugin.sysFS = legacy
	auth, ctx := renameReviewAuth, context.Background()
	_, err := plugin.Write(ctx, auth, "alpha", "/old.md", "# Notes\n\nAn ordinary file.", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	_, err = plugin.Rename(ctx, auth, "alpha", "/old.md", "/new.md", false)
	require.NoError(t, err)
	require.Equal(t, 1, legacy.renames)
	_, err = plugin.RenameAcrossProjects(ctx, auth, "alpha", "/new.md", "beta", "/target.md", false)
	require.True(t, files.IsCode(err, files.ErrCodeInvalidArgument), "%v", err)
	index, err := plugin.store.GetIndex(ctx, PageIndexStorageProject(auth, "alpha"))
	require.NoError(t, err)
	require.Contains(t, index, "/new.md")
	_, err = service.Read(ctx, auth, "alpha", "/new.md", 0, -1)
	require.NoError(t, err)
	_, err = service.Read(ctx, auth, "beta", "/target.md", 0, -1)
	require.True(t, files.IsCode(err, files.ErrCodeNotFound), "%v", err)
}
