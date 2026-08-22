package pageindex

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	errors "github.com/Laisky/errors/v2"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestWritePropagatesSynchronousPersistenceFailures verifies that PageIndex does
// not acknowledge a synchronous write when its tree, catalog, or metadata could
// not be persisted. The advertised zero-second freshness window requires a
// successful Write to be immediately searchable.
func TestWritePropagatesSynchronousPersistenceFailures(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		failAt int
	}{
		{name: "tree", failAt: 1},
		{name: "catalog", failAt: 2},
		{name: "metadata", failAt: 3},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			db, service := newPersistenceTestService(t)
			defer func() { require.NoError(t, db.Close()) }()

			settings := persistenceTestSettings()
			tokenizer, err := NewTokenizer(settings.LLM.IndexingModel)
			require.NoError(t, err)
			plugin, err := New(PluginDeps{
				UserFS:    service,
				SystemFS:  &failNthWriteSystemFS{failAt: testCase.failAt},
				Settings:  settings,
				LLM:       NewStubLLM(),
				Tokenizer: tokenizer,
			})
			require.NoError(t, err)
			require.NoError(t, plugin.Start(context.Background()))
			defer func() { require.NoError(t, plugin.Stop(context.Background())) }()

			_, err = plugin.Write(
				context.Background(),
				files.AuthContext{APIKey: "test-key", APIKeyHash: "test-hash", UserIdentity: "user:test"},
				"project",
				"/manual.md",
				"# Manual\n\nThe rollback token is ORBIT-17.",
				"utf-8",
				0,
				files.WriteModeTruncate,
			)
			require.Error(t, err, "a synchronous PageIndex write must fail when internal persistence fails")
			require.ErrorContains(t, err, "persist pageindex")
		})
	}
}

// TestAtomicPageIndexPublicationAndMutation covers P08/P09: production
// SystemFS mutations commit the user row and the tenant-scoped tree/catalog
// state under one project transaction.
func TestAtomicPageIndexPublicationAndMutation(t *testing.T) {
	db, service := newPersistenceTestService(t)
	defer func() { require.NoError(t, db.Close()) }()
	settings := persistenceTestSettings()
	tokenizer, err := NewTokenizer(settings.LLM.IndexingModel)
	require.NoError(t, err)
	systemFS, err := service.SystemNamespace(SysOwner)
	require.NoError(t, err)
	plugin, err := New(PluginDeps{
		UserFS:    service,
		SystemFS:  systemFS,
		Settings:  settings,
		LLM:       NewStubLLM(),
		Tokenizer: tokenizer,
	})
	require.NoError(t, err)
	require.NoError(t, plugin.Start(context.Background()))
	defer func() { require.NoError(t, plugin.Stop(context.Background())) }()
	auth := files.AuthContext{APIKey: "test-key", APIKeyHash: "tenant-a", UserIdentity: "user:test"}
	ctx := context.Background()

	_, err = plugin.Write(ctx, auth, "project", "/manual.md", "# Manual\n\nThe atomic publication token is ORBIT-17.", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	read, err := plugin.Read(ctx, auth, "project", "/manual.md", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "# Manual\n\nThe atomic publication token is ORBIT-17.", read.Content)
	var ragJobs int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM mcp_file_index_jobs WHERE apikey_hash = ? AND project = ? AND system_owner = ''`,
		auth.APIKeyHash, "project",
	).Scan(&ragJobs))
	require.Zero(t, ragJobs, "PageIndex writes must not enqueue RAG jobs")
	storageProject := PageIndexStorageProject(auth, "project")
	var systemFiles int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM mcp_files WHERE apikey_hash = ? AND project = ? AND system_owner = ? AND deleted = FALSE`,
		"system:"+SysOwner, storageProject, SysOwner,
	).Scan(&systemFiles))
	require.Equal(t, 3, systemFiles, "tree, index, and metadata must publish together")

	_, err = plugin.Rename(ctx, auth, "project", "/manual.md", "/renamed.md", false)
	require.NoError(t, err)
	var oldExists, newExists int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = '' AND deleted = FALSE`,
		auth.APIKeyHash, "project", "/manual.md",
	).Scan(&oldExists))
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = '' AND deleted = FALSE`,
		auth.APIKeyHash, "project", "/renamed.md",
	).Scan(&newExists))
	require.Equal(t, 0, oldExists)
	require.Equal(t, 1, newExists)
	index, err := plugin.store.GetIndex(ctx, storageProject)
	require.NoError(t, err)
	require.NotContains(t, index, "/manual.md")
	require.Contains(t, index, "/renamed.md")

	_, err = plugin.Delete(ctx, auth, "project", "/renamed.md", false)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM mcp_files WHERE apikey_hash = ? AND project = ? AND system_owner = ? AND deleted = FALSE`,
		"system:"+SysOwner, storageProject, SysOwner,
	).Scan(&systemFiles))
	require.Equal(t, 2, systemFiles, "delete must remove the tree and retain empty catalog metadata")
	index, err = plugin.store.GetIndex(ctx, storageProject)
	require.NoError(t, err)
	require.Empty(t, index)
}

// TestPageIndexCatalogIsTenantScopedAndRejectsStaleSource covers tenant
// isolation and source-hash validation on the search path.
func TestPageIndexCatalogIsTenantScopedAndRejectsStaleSource(t *testing.T) {
	db, service := newPersistenceTestService(t)
	defer func() { require.NoError(t, db.Close()) }()
	settings := persistenceTestSettings()
	tokenizer, err := NewTokenizer(settings.LLM.IndexingModel)
	require.NoError(t, err)
	systemFS, err := service.SystemNamespace(SysOwner)
	require.NoError(t, err)
	plugin, err := New(PluginDeps{
		UserFS:    service,
		SystemFS:  systemFS,
		Settings:  settings,
		LLM:       NewStubLLM(),
		Tokenizer: tokenizer,
	})
	require.NoError(t, err)
	require.NoError(t, plugin.Start(context.Background()))
	defer func() { require.NoError(t, plugin.Stop(context.Background())) }()
	ctx := context.Background()
	authA := files.AuthContext{APIKey: "key-a", APIKeyHash: "tenant-a", UserIdentity: "user:a"}
	authB := files.AuthContext{APIKey: "key-b", APIKeyHash: "tenant-b", UserIdentity: "user:b"}
	for auth, marker := range map[files.AuthContext]string{authA: "alpha", authB: "beta"} {
		_, err = plugin.Write(ctx, auth, "project", "/manual.md", "# Manual\n\nThe "+marker+" tenant document.", "utf-8", 0, files.WriteModeTruncate)
		require.NoError(t, err)
	}

	resultA, err := plugin.Search(ctx, authA, "project", "document", "", 10)
	require.NoError(t, err)
	require.NotEmpty(t, resultA.Chunks)
	for _, chunk := range resultA.Chunks {
		require.NotContains(t, chunk.ChunkContent, "beta")
	}
	resultB, err := plugin.Search(ctx, authB, "project", "document", "", 10)
	require.NoError(t, err)
	require.NotEmpty(t, resultB.Chunks)
	for _, chunk := range resultB.Chunks {
		require.NotContains(t, chunk.ChunkContent, "alpha")
	}

	_, err = service.Write(ctx, authA, "project", "/manual.md", "# Replaced\n\nThe source hash changed.", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	stale, err := plugin.Search(ctx, authA, "project", "document", "", 10)
	require.NoError(t, err)
	require.Empty(t, stale.Chunks, "stale catalog entries must not return old tree content")
}

type failNthWriteSystemFS struct {
	failAt int
	writes int
}

func (*failNthWriteSystemFS) Read(context.Context, string, string) ([]byte, error) {
	return nil, files.NewError(files.ErrCodeNotFound, "not found", false)
}

func (s *failNthWriteSystemFS) Write(context.Context, string, string, []byte) error {
	s.writes++
	if s.writes == s.failAt {
		return errors.Errorf("forced system write failure %d", s.failAt)
	}
	return nil
}

func (*failNthWriteSystemFS) Delete(context.Context, string, string) error { return nil }

func (*failNthWriteSystemFS) List(context.Context, string, string) ([]string, error) { return nil, nil }

func newPersistenceTestService(t *testing.T) (*sql.DB, *files.Service) {
	t.Helper()
	dsn := fmt.Sprintf("file:pageindex-persistence-%s-%d?mode=memory&cache=shared&_foreign_keys=on", t.Name(), time.Now().UnixNano())
	db, err := sql.Open("sqlite3", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	service, err := files.NewService(db, files.Settings{
		MaxPayloadBytes:  2_000_000,
		MaxFileBytes:     10_000_000,
		MaxProjectBytes:  100_000_000,
		ListLimitDefault: 20,
		ListLimitMax:     100,
		LockTimeout:      time.Second,
		DeleteRetention:  time.Hour,
		Search:           files.SearchSettings{Enabled: false},
		Index:            files.IndexSettings{Workers: 1, BatchSize: 1, ChunkBytes: 4096},
	}, nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	return db, service
}

func persistenceTestSettings() Settings {
	return Settings{
		Indexer: IndexerSettings{
			TimeoutIndex:   time.Second,
			TimeoutQuery:   time.Second,
			MaxConcurrency: 1,
			Retry:          RetrySettings{MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
			Cache:          CacheSettings{Enabled: false},
		},
		LLM: LLMSettings{IndexingModel: "gpt-5.4-mini", RetrieveModel: "gpt-5.4-mini", APIKey: "test-key"},
		Algo: AlgoSettings{
			TocCheckPageNum:        20,
			MaxPageNumEachNode:     10,
			MaxTokenNumEachNode:    20_000,
			GenerateNodeSummary:    false,
			GenerateDocDescription: false,
		},
		TreeQuery: TreeQuerySettings{MaxSteps: 8, MaxTokens: 20_000, CandidateDocs: 5},
		PDF:       PDFSettings{TextParser: "pdfcpu", OutlineParser: "pdfcpu"},
	}
}
