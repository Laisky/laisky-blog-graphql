package pageindex

import (
	"context"
	"sync"
	"testing"

	errors "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestWriteCatalogFailureKeepsRetryableTree verifies a failed catalog update
// does not destructively delete the deterministic tree needed by a retry.
func TestWriteCatalogFailureKeepsRetryableTree(t *testing.T) {
	db, service := newPersistenceTestService(t)
	defer func() { require.NoError(t, db.Close()) }()
	plugin, faultFS, auth := newRetryTestPlugin(t, service)
	defer func() { require.NoError(t, plugin.Stop(context.Background())) }()

	faultFS.FailNextWrite(indexPath())
	_, err := plugin.Write(
		context.Background(), auth, "project", "/manual.md",
		"# Manual\n\nThe rollback token is ORBIT-17.", "utf-8", 0, files.WriteModeTruncate,
	)
	require.Error(t, err)

	docID := docIDFromAuth(auth, "project", "/manual.md")
	tree, treeErr := plugin.store.GetTree(context.Background(), "project", docID)
	require.NoError(t, treeErr, "the tree remains durable so retry can converge")
	require.NotEmpty(t, tree.Structure)

	_, err = plugin.Write(
		context.Background(), auth, "project", "/manual.md",
		"# Manual\n\nThe rollback token is ORBIT-17.", "utf-8", 0, files.WriteModeTruncate,
	)
	require.NoError(t, err)
	index, err := plugin.store.GetIndex(context.Background(), "project")
	require.NoError(t, err)
	require.Contains(t, index, "/manual.md")
}

// TestDeleteCatalogFailureIsRetryable verifies a failed catalog mutation does
// not delete the user file first and strand an index entry that retries cannot remove.
func TestDeleteCatalogFailureIsRetryable(t *testing.T) {
	db, service := newPersistenceTestService(t)
	defer func() { require.NoError(t, db.Close()) }()
	plugin, faultFS, auth := newRetryTestPlugin(t, service)
	defer func() { require.NoError(t, plugin.Stop(context.Background())) }()

	_, err := plugin.Write(
		context.Background(), auth, "project", "/manual.md",
		"# Manual\n\nThe rollback token is ORBIT-17.", "utf-8", 0, files.WriteModeTruncate,
	)
	require.NoError(t, err)

	faultFS.FailNextWrite(indexPath())
	_, err = plugin.Delete(context.Background(), auth, "project", "/manual.md", false)
	require.Error(t, err)
	stat, statErr := plugin.Stat(context.Background(), auth, "project", "/manual.md")
	require.NoError(t, statErr)
	require.True(t, stat.Exists, "the user file remains until PageIndex cleanup is durable")

	_, err = plugin.Delete(context.Background(), auth, "project", "/manual.md", false)
	require.NoError(t, err, "retry must converge after the one-shot catalog fault")
	stat, statErr = plugin.Stat(context.Background(), auth, "project", "/manual.md")
	require.NoError(t, statErr)
	require.False(t, stat.Exists)
	index, err := plugin.store.GetIndex(context.Background(), "project")
	require.NoError(t, err)
	require.NotContains(t, index, "/manual.md")
}

// TestRenameCatalogFailureIsRetryable verifies catalog persistence is attempted
// before moving the user file so the same rename request can be retried safely.
func TestRenameCatalogFailureIsRetryable(t *testing.T) {
	db, service := newPersistenceTestService(t)
	defer func() { require.NoError(t, db.Close()) }()
	plugin, faultFS, auth := newRetryTestPlugin(t, service)
	defer func() { require.NoError(t, plugin.Stop(context.Background())) }()

	_, err := plugin.Write(
		context.Background(), auth, "project", "/source.md",
		"# Manual\n\nThe rollback token is ORBIT-17.", "utf-8", 0, files.WriteModeTruncate,
	)
	require.NoError(t, err)

	faultFS.FailNextWrite(indexPath())
	_, err = plugin.Rename(context.Background(), auth, "project", "/source.md", "/destination.md", false)
	require.Error(t, err)
	source, sourceErr := plugin.Stat(context.Background(), auth, "project", "/source.md")
	require.NoError(t, sourceErr)
	require.True(t, source.Exists, "the user path remains unchanged when catalog persistence fails")

	_, err = plugin.Rename(context.Background(), auth, "project", "/source.md", "/destination.md", false)
	require.NoError(t, err, "retry must converge after the one-shot catalog fault")
	destination, destinationErr := plugin.Stat(context.Background(), auth, "project", "/destination.md")
	require.NoError(t, destinationErr)
	require.True(t, destination.Exists)
	index, err := plugin.store.GetIndex(context.Background(), "project")
	require.NoError(t, err)
	require.NotContains(t, index, "/source.md")
	require.Contains(t, index, "/destination.md")
}

func newRetryTestPlugin(t *testing.T, service *files.Service) (*Plugin, *failOnceSystemFS, files.AuthContext) {
	t.Helper()
	systemFS, err := service.SystemNamespace(SysOwner)
	require.NoError(t, err)
	faultFS := &failOnceSystemFS{delegate: systemFS}
	settings := persistenceTestSettings()
	tokenizer, err := NewTokenizer(settings.LLM.IndexingModel)
	require.NoError(t, err)
	llm := NewStubLLM()
	llm.SetDefault(&Response{
		Text: `{"ranges":[{"start":1,"end":1000,"reason":"retry regression"}]}`,
		Usage: Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	})
	plugin, err := New(PluginDeps{
		UserFS: service,
		SystemFS: faultFS,
		Settings: settings,
		LLM: llm,
		Tokenizer: tokenizer,
	})
	require.NoError(t, err)
	require.NoError(t, plugin.Start(context.Background()))
	return plugin, faultFS, files.AuthContext{
		APIKey: "retry-test",
		APIKeyHash: "retry-test",
		UserIdentity: "user:retry-test",
	}
}

type failOnceSystemFS struct {
	delegate files.SystemFS
	mu sync.Mutex
	failWritePath string
}

func (s *failOnceSystemFS) FailNextWrite(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failWritePath = path
}

func (s *failOnceSystemFS) Read(ctx context.Context, project, path string) ([]byte, error) {
	return s.delegate.Read(ctx, project, path)
}

func (s *failOnceSystemFS) Write(ctx context.Context, project, path string, content []byte) error {
	s.mu.Lock()
	if path == s.failWritePath {
		s.failWritePath = ""
		s.mu.Unlock()
		return errors.Errorf("forced one-shot write failure for %s", path)
	}
	s.mu.Unlock()
	return s.delegate.Write(ctx, project, path, content)
}

func (s *failOnceSystemFS) Delete(ctx context.Context, project, path string) error {
	return s.delegate.Delete(ctx, project, path)
}

func (s *failOnceSystemFS) List(ctx context.Context, project, prefix string) ([]string, error) {
	return s.delegate.List(ctx, project, prefix)
}
