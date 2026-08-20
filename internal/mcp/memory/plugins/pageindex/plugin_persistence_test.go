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
				UserFS: service,
				SystemFS: &failNthWriteSystemFS{failAt: testCase.failAt},
				Settings: settings,
				LLM: NewStubLLM(),
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

type failNthWriteSystemFS struct {
	failAt int
	writes int
}

func (s *failNthWriteSystemFS) Read(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("not found")
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
		MaxPayloadBytes: 2_000_000,
		MaxFileBytes: 10_000_000,
		MaxProjectBytes: 100_000_000,
		ListLimitDefault: 20,
		ListLimitMax: 100,
		LockTimeout: time.Second,
		DeleteRetention: time.Hour,
		Search: files.SearchSettings{Enabled: false},
		Index: files.IndexSettings{Workers: 1, BatchSize: 1, ChunkBytes: 4096},
	}, nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	return db, service
}

func persistenceTestSettings() Settings {
	return Settings{
		Indexer: IndexerSettings{
			TimeoutIndex: time.Second,
			TimeoutQuery: time.Second,
			MaxConcurrency: 1,
			Retry: RetrySettings{MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
			Cache: CacheSettings{Enabled: false},
		},
		LLM: LLMSettings{IndexingModel: "gpt-5.4-mini", RetrieveModel: "gpt-5.4-mini", APIKey: "test-key"},
		Algo: AlgoSettings{
			TocCheckPageNum: 20,
			MaxPageNumEachNode: 10,
			MaxTokenNumEachNode: 20_000,
			GenerateNodeSummary: false,
			GenerateDocDescription: false,
		},
		TreeQuery: TreeQuerySettings{MaxSteps: 8, MaxTokens: 20_000, CandidateDocs: 5},
		PDF: PDFSettings{TextParser: "pdfcpu", OutlineParser: "pdfcpu"},
	}
}
