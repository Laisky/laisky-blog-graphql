package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/jackc/pgx/v5/pgconn"
	pgvector "github.com/pgvector/pgvector-go"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// Clock abstracts time source for deterministic tests.
type Clock func() time.Time

// ExtractInput captures the normalized parameters passed to the service.
type ExtractInput struct {
	UserID    string
	TaskID    string
	APIKey    string
	Query     string
	Materials string
	TopK      int
}

// Service coordinates chunking, storage, and retrieval for the extract_key_info tool.
type Service struct {
	db       *gorm.DB
	embedder Embedder
	chunker  Chunker
	settings Settings
	logger   logSDK.Logger
	clock    Clock
}

// NewService wires the dependencies and runs the required schema migrations.
func NewService(db *gorm.DB, embedder Embedder, chunker Chunker, settings Settings, logger logSDK.Logger) (*Service, error) {
	if db == nil {
		return nil, errors.New("gorm db is required")
	}
	if embedder == nil {
		return nil, errors.New("embedding client is required")
	}
	if chunker == nil {
		chunker = ParagraphChunker{}
	}
	if logger == nil {
		logger = log.Logger.Named("mcp_rag_service")
	}

	svc := &Service{
		db:       db,
		embedder: embedder,
		chunker:  chunker,
		settings: settings,
		logger:   logger,
		clock: func() time.Time {
			return time.Now().UTC()
		},
	}

	if err := runRAGMigrations(context.Background(), db, logger); err != nil {
		return nil, errors.WithStack(err)
	}

	return svc, nil
}

func runRAGMigrations(ctx context.Context, db *gorm.DB, logger logSDK.Logger) error {
	if logger == nil {
		logger = log.Logger.Named("mcp_rag_service")
	}

	logger.Debug("ensuring pgvector extension for rag service")
	if err := ensureVectorExtension(ctx, db, logger); err != nil {
		return errors.Wrap(err, "ensure pgvector extension")
	}
	logger.Debug("pgvector extension ensured for rag service")

	logger.Debug("running rag auto migrations")
	if err := db.WithContext(ctx).AutoMigrate(&Task{}, &Chunk{}, &Embedding{}, &BM25Row{}); err != nil {
		return errors.Wrap(err, "auto migrate rag tables")
	}
	logger.Debug("rag auto migrations finished")

	return nil
}

func (s *Service) loggerFromContext(ctx context.Context) logSDK.Logger {
	if ctx != nil {
		if ctxLogger := gmw.GetLogger(ctx); ctxLogger != nil {
			return ctxLogger
		}
		if ctxLogger, ok := ctx.Value(ctxkeys.Logger).(logSDK.Logger); ok && ctxLogger != nil {
			return ctxLogger
		}
	}
	if s.logger != nil {
		return s.logger
	}
	return log.Logger.Named("mcp_rag_service")
}

func ensureVectorExtension(ctx context.Context, db *gorm.DB, logger logSDK.Logger) error {
	if db == nil {
		return errors.New("gorm db is nil")
	}
	if !isPostgresDialect(db) {
		return nil
	}

	if err := db.WithContext(ctx).Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		if shouldFallbackToPgvector(err) {
			if logger != nil {
				logger.Debug("pgvector extension unavailable under name 'vector', retrying with legacy name")
			}
			if execErr := db.WithContext(ctx).Exec("CREATE EXTENSION IF NOT EXISTS pgvector").Error; execErr != nil {
				return errors.Wrap(execErr, "create pgvector extension")
			}
			return nil
		}
		return errors.Wrap(err, "create vector extension")
	}
	return nil
}

func isPostgresDialect(db *gorm.DB) bool {
	if db == nil || db.Dialector == nil {
		return false
	}
	return strings.EqualFold(db.Dialector.Name(), "postgres")
}

func shouldFallbackToPgvector(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "58P01", "42704":
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "extension \"vector\"") && strings.Contains(msg, "not") && strings.Contains(msg, "available")
}

// ExtractKeyInfo orchestrates ingestion (if needed) and hybrid retrieval for the request.
func (s *Service) ExtractKeyInfo(ctx context.Context, input ExtractInput) ([]string, error) {
	if err := s.validateInput(input); err != nil {
		return nil, errors.WithStack(err)
	}

	task, err := s.ensureTask(ctx, input.UserID, input.TaskID)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if err := s.ensureChunks(ctx, task, input); err != nil {
		return nil, errors.WithStack(err)
	}

	queryTokens := tokenize(input.Query)
	queryVecs, err := s.embedder.EmbedTexts(ctx, input.APIKey, []string{input.Query})
	if err != nil {
		return nil, errors.Wrap(err, "embed query")
	}
	if len(queryVecs) == 0 {
		return nil, errors.New("embedding provider returned no query vector")
	}
	queryVec := queryVecs[0]

	candidates, err := s.fetchCandidates(ctx, task.ID, queryVec, max(16, input.TopK*4))
	if err != nil {
		return nil, errors.WithStack(err)
	}

	contexts := s.rankAndSelect(candidates, queryVec, queryTokens, input.TopK)
	return contexts, nil
}

func (s *Service) validateInput(input ExtractInput) error {
	if strings.TrimSpace(input.UserID) == "" {
		return errors.New("user id cannot be empty")
	}
	if strings.TrimSpace(input.TaskID) == "" {
		return errors.New("task id cannot be empty")
	}
	if strings.TrimSpace(input.APIKey) == "" {
		return errors.New("api key cannot be empty")
	}
	if strings.TrimSpace(input.Query) == "" {
		return errors.New("query cannot be empty")
	}
	if strings.TrimSpace(input.Materials) == "" {
		return errors.New("materials cannot be empty")
	}
	if len(input.Materials) > s.settings.MaxMaterialsSize {
		return errors.Errorf("materials exceed maximum size (%d bytes)", s.settings.MaxMaterialsSize)
	}
	if input.TopK <= 0 || input.TopK > s.settings.TopKLimit {
		return errors.Errorf("topK must be between 1 and %d", s.settings.TopKLimit)
	}
	return nil
}

func (s *Service) ensureTask(ctx context.Context, userID, taskID string) (*Task, error) {
	var task Task
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND task_id = ?", userID, taskID).
		First(&task).Error
	if err == nil {
		return &task, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.Wrap(err, "query rag task")
	}

	task = Task{UserID: userID, TaskID: taskID}
	if err := s.db.WithContext(ctx).Create(&task).Error; err != nil {
		return nil, errors.Wrap(err, "create rag task")
	}
	logger := s.loggerFromContext(ctx)
	logger.Info("rag task created", zap.String("user_id", userID), zap.String("task_id", taskID))
	return &task, nil
}

func (s *Service) ensureChunks(ctx context.Context, task *Task, input ExtractInput) error {
	fragments := s.chunker.Split(input.Materials, s.settings.MaxChunkChars)
	if len(fragments) == 0 {
		return errors.New("no chunks generated from materials")
	}

	hash := sha256.Sum256([]byte(strings.Join(cleanedFragments(fragments), "\n")))
	materialsHash := hex.EncodeToString(hash[:])

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&Chunk{}).
			Where("task_id = ? AND materials_hash = ?", task.ID, materialsHash).
			Count(&count).Error; err != nil {
			return errors.Wrap(err, "check existing chunks")
		}
		if count > 0 {
			return nil
		}

		texts := make([]string, 0, len(fragments))
		for _, fragment := range fragments {
			texts = append(texts, fragment.Cleaned)
		}
		embeddings, err := s.embedder.EmbedTexts(ctx, input.APIKey, texts)
		if err != nil {
			return errors.Wrap(err, "embed materials")
		}
		if len(embeddings) != len(fragments) {
			return errors.New("embedding count mismatch")
		}

		now := s.clock()
		for idx, fragment := range fragments {
			chunk := Chunk{
				TaskID:        task.ID,
				MaterialsHash: materialsHash,
				ChunkIndex:    fragment.Index,
				Text:          fragment.Text,
				CleanedText:   fragment.Cleaned,
				Metadata: datatypes.JSONMap{
					"user_id": task.UserID,
					"task_id": task.TaskID,
					"hash":    materialsHash,
				},
				CreatedAt: now,
				UpdatedAt: now,
			}
			if err := tx.Create(&chunk).Error; err != nil {
				return errors.Wrap(err, "insert chunk")
			}

			embed := Embedding{
				ChunkID:   chunk.ID,
				Vector:    embeddings[idx],
				Model:     s.settings.EmbeddingModel,
				CreatedAt: now,
				UpdatedAt: now,
			}
			if err := tx.Create(&embed).Error; err != nil {
				return errors.Wrap(err, "insert embedding")
			}

			tokensJSON, err := json.Marshal(fragment.Tokens)
			if err != nil {
				return errors.Wrap(err, "encode tokens")
			}
			bm25 := BM25Row{
				ChunkID:    chunk.ID,
				Tokens:     datatypes.JSON(tokensJSON),
				TokenCount: len(fragment.Tokens),
				Tokenizer:  "builtin",
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			if err := tx.Create(&bm25).Error; err != nil {
				return errors.Wrap(err, "insert bm25 row")
			}
		}

		logger := s.loggerFromContext(ctx)
		logger.Info("rag materials ingested",
			zap.String("user_id", task.UserID),
			zap.String("task_id", task.TaskID),
			zap.Int("chunks", len(fragments)),
		)

		return nil
	})
}

func (s *Service) fetchCandidates(ctx context.Context, taskID int64, queryVec pgvector.Vector, limit int) ([]candidateChunk, error) {
	logger := s.loggerFromContext(ctx)
	logger.Debug("fetching rag candidates", zap.Int64("task_id", taskID), zap.Int("limit", limit))
	rows := make([]candidateChunk, 0, limit)
	err := s.db.WithContext(ctx).
		Raw(`
		    SELECT c.id, c.text, c.cleaned_text, e.vector AS embedding, b.tokens
            FROM mcp_rag_chunks c
            JOIN mcp_rag_embeddings e ON e.chunk_id = c.id
            LEFT JOIN mcp_rag_bm25 b ON b.chunk_id = c.id
            WHERE c.task_id = ?
		    ORDER BY e.vector <=> ? ASC
            LIMIT ?
        `, taskID, queryVec, limit).
		Scan(&rows).Error
	if err != nil {
		return nil, errors.Wrap(err, "query rag candidates")
	}
	logger.Debug("rag candidates fetched", zap.Int64("task_id", taskID), zap.Int("count", len(rows)))
	return rows, nil
}

func (s *Service) rankAndSelect(candidates []candidateChunk, queryVec pgvector.Vector, queryTokens []string, topK int) []string {
	if len(candidates) == 0 {
		return nil
	}

	tokenSet := make(map[string]struct{}, len(queryTokens))
	for _, t := range queryTokens {
		tokenSet[t] = struct{}{}
	}

	type scored struct {
		text  string
		score float64
	}

	scoredChunks := make([]scored, 0, len(candidates))
	for _, candidate := range candidates {
		semantic := cosineSimilarity(queryVec, candidate.Embedding)
		lexical := lexicalScore(candidate.tokens(), tokenSet)
		score := s.settings.SemanticWeight*semantic + s.settings.LexicalWeight*lexical
		scoredChunks = append(scoredChunks, scored{text: candidate.Text, score: score})
	}

	sort.Slice(scoredChunks, func(i, j int) bool {
		if scoredChunks[i].score == scoredChunks[j].score {
			return len(scoredChunks[i].text) < len(scoredChunks[j].text)
		}
		return scoredChunks[i].score > scoredChunks[j].score
	})

	if topK > len(scoredChunks) {
		topK = len(scoredChunks)
	}
	contexts := make([]string, 0, topK)
	for i := 0; i < topK; i++ {
		contexts = append(contexts, scoredChunks[i].text)
	}
	return contexts
}

func cleanedFragments(fragments []ChunkFragment) []string {
	values := make([]string, 0, len(fragments))
	for _, fragment := range fragments {
		values = append(values, fragment.Cleaned)
	}
	return values
}

type candidateChunk struct {
	ChunkID    int64           `gorm:"column:id"`
	Text       string          `gorm:"column:text"`
	Cleaned    string          `gorm:"column:cleaned_text"`
	Embedding  pgvector.Vector `gorm:"column:embedding"`
	TokenBytes datatypes.JSON  `gorm:"column:tokens"`
}

func (c candidateChunk) tokens() []string {
	if len(c.TokenBytes) == 0 {
		return nil
	}
	var tokens []string
	_ = json.Unmarshal(c.TokenBytes, &tokens)
	return tokens
}

func cosineSimilarity(a, b pgvector.Vector) float64 {
	av := a.Slice()
	bv := b.Slice()
	if len(av) == 0 || len(bv) == 0 {
		return 0
	}
	var dot float64
	var normA float64
	var normB float64
	for i := 0; i < len(av) && i < len(bv); i++ {
		va := float64(av[i])
		vb := float64(bv[i])
		dot += va * vb
		normA += va * va
		normB += vb * vb
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (sqrt(normA) * sqrt(normB))
}

func lexicalScore(tokens []string, query map[string]struct{}) float64 {
	if len(tokens) == 0 || len(query) == 0 {
		return 0
	}
	matches := 0
	for _, token := range tokens {
		if _, ok := query[token]; ok {
			matches++
		}
	}
	return float64(matches) / float64(len(tokens))
}

func sqrt(value float64) float64 {
	return math.Sqrt(value)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
