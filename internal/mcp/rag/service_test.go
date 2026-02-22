package rag

import (
	"context"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
	pgvector "github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

type captureRAGEmbedder struct {
	keys []string
}

// EmbedTexts captures API keys used for embedding requests and returns deterministic vectors.
func (e *captureRAGEmbedder) EmbedTexts(_ context.Context, apiKey string, inputs []string) ([]pgvector.Vector, error) {
	e.keys = append(e.keys, apiKey)
	result := make([]pgvector.Vector, 0, len(inputs))
	for range inputs {
		result = append(result, pgvector.NewVector([]float32{1, 0, 0}))
	}
	return result, nil
}

func TestEnsureVectorExtensionPostgresSuccess(t *testing.T) {
	t.Parallel()

	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer sqlDB.Close()

	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sqlDB,
	}))
	require.NoError(t, err)

	mock.ExpectExec(`CREATE EXTENSION IF NOT EXISTS vector`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = ensureVectorExtension(context.Background(), gdb, log.Logger.Named("test"))
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEnsureVectorExtensionFallback(t *testing.T) {
	t.Parallel()

	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer sqlDB.Close()

	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sqlDB,
	}))
	require.NoError(t, err)

	pgErr := &pgconn.PgError{Code: "58P01", Message: "extension \"vector\" is not available"}

	mock.ExpectExec(`CREATE EXTENSION IF NOT EXISTS vector`).
		WillReturnError(pgErr)
	mock.ExpectExec(`CREATE EXTENSION IF NOT EXISTS pgvector`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = ensureVectorExtension(context.Background(), gdb, log.Logger.Named("test"))
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEnsureVectorExtensionSkipNonPostgres(t *testing.T) {
	t.Parallel()

	gdb, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)

	err = ensureVectorExtension(context.Background(), gdb, log.Logger.Named("test"))
	require.NoError(t, err)
}

func TestFetchCandidatesUsesVectorColumn(t *testing.T) {
	t.Parallel()

	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer sqlDB.Close()

	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}))
	require.NoError(t, err)

	svc := &Service{
		db:     gdb,
		logger: log.Logger.Named("test"),
	}

	queryVec := pgvector.NewVector([]float32{0.1, 0.2})

	pattern := regexp.MustCompile(`SELECT c\.id, c\.text, c\.cleaned_text, e\.vector AS embedding, b\.tokens[\s\S]+ORDER BY e\.vector <=> \$[0-9]+ ASC[\s\S]+LIMIT \$[0-9]+`)
	rows := sqlmock.NewRows([]string{"id", "text", "cleaned_text", "embedding", "tokens"}).
		AddRow(int64(1), "chunk text", "chunk cleaned", queryVec, datatypes.JSON([]byte(`["jwt"]`)))

	mock.ExpectQuery(pattern.String()).
		WithArgs(int64(1), sqlmock.AnyArg(), 5).
		WillReturnRows(rows)

	candidates, err := svc.fetchCandidates(context.Background(), 1, queryVec, 5)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, "chunk text", candidates[0].Text)
	require.Equal(t, queryVec, candidates[0].Embedding)
	require.Equal(t, []string{"jwt"}, candidates[0].tokens())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestExtractKeyInfoUsesRequestAPIKeyForEmbedding(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)

	embedder := &captureRAGEmbedder{}
	settings := LoadSettingsFromConfig()
	settings.Enabled = true
	settings.TopKDefault = 2
	settings.TopKLimit = 10
	settings.MaxMaterialsSize = 1024 * 1024
	settings.MaxChunkChars = 80

	svc, err := NewService(db, embedder, ParagraphChunker{}, settings, log.Logger.Named("rag_key_propagation_test"))
	require.NoError(t, err)

	_, err = svc.ExtractKeyInfo(context.Background(), ExtractInput{
		UserID:    "user:tenant",
		TaskID:    "task-1",
		APIKey:    "tenant-request-key",
		Query:     "where is alpha",
		Materials: "alpha content in first paragraph\n\nsecond paragraph",
		TopK:      2,
	})
	if err != nil {
		require.Contains(t, err.Error(), "query rag candidates")
	}
	require.GreaterOrEqual(t, len(embedder.keys), 2)
	for _, key := range embedder.keys {
		require.Equal(t, "tenant-request-key", key)
	}
}

func TestEnsureTaskFallsBackToLegacyUserID(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:rag_legacy_fallback?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)

	if err := db.AutoMigrate(&Task{}); err != nil {
		require.NoError(t, err)
	}

	apiKey := "sk-legacy-compat-key"
	legacyUserID := legacyUserIDFromAPIKey(apiKey)
	require.NotEmpty(t, legacyUserID)

	task := Task{UserID: legacyUserID, TaskID: "workspace"}
	require.NoError(t, db.Create(&task).Error)

	svc := &Service{db: db, logger: log.Logger.Named("rag_legacy_fallback_test")}
	canonicalAuth, err := mcpauth.DeriveFromAPIKey(apiKey)
	require.NoError(t, err)

	resolved, err := svc.ensureTask(context.Background(), canonicalAuth.UserID, "workspace", apiKey)
	require.NoError(t, err)
	require.Equal(t, task.ID, resolved.ID)
	require.Equal(t, legacyUserID, resolved.UserID)
}
