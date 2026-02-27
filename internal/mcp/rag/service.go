package rag

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	db       *sql.DB
	dialect  sqlDialect
	embedder Embedder
	chunker  Chunker
	settings Settings
	logger   logSDK.Logger
	clock    Clock
}

// NewService wires the dependencies and runs the required schema migrations.
func NewService(db *sql.DB, embedder Embedder, chunker Chunker, settings Settings, logger logSDK.Logger) (*Service, error) {
	if db == nil {
		return nil, errors.New("sql db is required")
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
		dialect:  detectSQLDialect(db),
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

// runRAGMigrations ensures table/index schemas needed by RAG retrieval are present.
func runRAGMigrations(ctx context.Context, db *sql.DB, logger logSDK.Logger) error {
	if logger == nil {
		logger = log.Logger.Named("mcp_rag_service")
	}

	dialect := detectSQLDialect(db)
	driverType := ""
	if db != nil {
		driverType = fmt.Sprintf("%T", db.Driver())
	}
	logger.Debug("detected rag sql dialect",
		zap.String("dialect", dialect.String()),
		zap.String("driver_type", driverType),
	)

	logger.Debug("ensuring pgvector extension for rag service")
	if err := ensureVectorExtension(ctx, db, logger); err != nil {
		return errors.Wrap(err, "ensure pgvector extension")
	}
	logger.Debug("pgvector extension ensured for rag service")

	logger.Debug("running rag sql migrations")
	for idx, statement := range ragMigrationStatements(dialect) {
		logger.Debug("executing rag migration statement",
			zap.Int("statement_index", idx),
			zap.String("dialect", dialect.String()),
		)
		if _, err := db.ExecContext(ctx, statement); err != nil {
			logger.Debug("rag migration statement failed",
				zap.Int("statement_index", idx),
				zap.String("dialect", dialect.String()),
				zap.String("statement", statement),
				zap.Error(err),
			)
			return errors.Wrapf(err, "run rag migration statement #%d with dialect %s", idx, dialect.String())
		}
	}
	logger.Debug("rag sql migrations finished")

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

// ensureVectorExtension enables the pgvector extension on PostgreSQL connections.
func ensureVectorExtension(ctx context.Context, db *sql.DB, logger logSDK.Logger) error {
	if db == nil {
		return errors.New("sql db is nil")
	}
	dialect := detectSQLDialect(db)
	if dialect != sqlDialectPostgres {
		if logger != nil {
			driverType := fmt.Sprintf("%T", db.Driver())
			logger.Debug("skip pgvector extension because sql dialect is not postgres",
				zap.String("dialect", dialect.String()),
				zap.String("driver_type", driverType),
			)
		}
		return nil
	}

	if _, err := db.ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		if shouldFallbackToPgvector(err) {
			if logger != nil {
				logger.Debug("pgvector extension unavailable under name 'vector', retrying with legacy name")
			}
			if _, execErr := db.ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS pgvector"); execErr != nil {
				return errors.Wrap(execErr, "create pgvector extension")
			}
			return nil
		}
		return errors.Wrap(err, "create vector extension")
	}
	return nil
}

// isPostgresDialect reports whether the database is PostgreSQL-compatible.
func isPostgresDialect(db *sql.DB) bool {
	return detectSQLDialect(db) == sqlDialectPostgres
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

// ragMigrationStatements returns idempotent DDL statements for RAG tables and indexes.
func ragMigrationStatements(dialect sqlDialect) []string {
	if dialect != sqlDialectSQLite {
		return []string{
			`CREATE TABLE IF NOT EXISTS mcp_rag_tasks (
				id BIGSERIAL PRIMARY KEY,
				user_id VARCHAR(128) NOT NULL,
				task_id VARCHAR(128) NOT NULL,
				created_at TIMESTAMPTZ NOT NULL,
				updated_at TIMESTAMPTZ NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_rag_tasks_user_task ON mcp_rag_tasks(user_id, task_id)`,
			`CREATE TABLE IF NOT EXISTS mcp_rag_chunks (
				id BIGSERIAL PRIMARY KEY,
				task_id BIGINT NOT NULL,
				materials_hash VARCHAR(96) NOT NULL,
				chunk_index INTEGER NOT NULL,
				text TEXT NOT NULL,
				cleaned_text TEXT NOT NULL,
				metadata JSONB,
				created_at TIMESTAMPTZ NOT NULL,
				updated_at TIMESTAMPTZ NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_rag_chunks_task_hash ON mcp_rag_chunks(task_id, materials_hash)`,
			`CREATE INDEX IF NOT EXISTS idx_rag_chunks_unique ON mcp_rag_chunks(chunk_index)`,
			`CREATE TABLE IF NOT EXISTS mcp_rag_embeddings (
				chunk_id BIGINT PRIMARY KEY,
				vector VECTOR(1536) NOT NULL,
				model VARCHAR(128) NOT NULL,
				created_at TIMESTAMPTZ NOT NULL,
				updated_at TIMESTAMPTZ NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS mcp_rag_bm25 (
				chunk_id BIGINT PRIMARY KEY,
				tokens JSONB NOT NULL,
				token_count INTEGER NOT NULL,
				tokenizer VARCHAR(64) NOT NULL,
				created_at TIMESTAMPTZ NOT NULL,
				updated_at TIMESTAMPTZ NOT NULL
			)`,
		}
	}

	return []string{
		`CREATE TABLE IF NOT EXISTS mcp_rag_tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT NOT NULL,
			task_id TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_rag_tasks_user_task ON mcp_rag_tasks(user_id, task_id)`,
		`CREATE TABLE IF NOT EXISTS mcp_rag_chunks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id INTEGER NOT NULL,
			materials_hash TEXT NOT NULL,
			chunk_index INTEGER NOT NULL,
			text TEXT NOT NULL,
			cleaned_text TEXT NOT NULL,
			metadata TEXT,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_rag_chunks_task_hash ON mcp_rag_chunks(task_id, materials_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_rag_chunks_unique ON mcp_rag_chunks(chunk_index)`,
		`CREATE TABLE IF NOT EXISTS mcp_rag_embeddings (
			chunk_id INTEGER PRIMARY KEY,
			vector TEXT NOT NULL,
			model TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_rag_bm25 (
			chunk_id INTEGER PRIMARY KEY,
			tokens TEXT NOT NULL,
			token_count INTEGER NOT NULL,
			tokenizer TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
	}
}

// ExtractKeyInfo orchestrates ingestion (if needed) and hybrid retrieval for the request.
func (s *Service) ExtractKeyInfo(ctx context.Context, input ExtractInput) ([]string, error) {
	if err := s.validateInput(input); err != nil {
		return nil, errors.WithStack(err)
	}

	task, err := s.ensureTask(ctx, input.UserID, input.TaskID, input.APIKey)
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

// ensureTask resolves the canonical task row and falls back to legacy user IDs when needed.
func (s *Service) ensureTask(ctx context.Context, userID, taskID, apiKey string) (*Task, error) {
	var task Task
	err := s.getTaskByUserAndTaskID(ctx, userID, taskID, &task)
	if err == nil {
		return &task, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, errors.Wrap(err, "query rag task")
	}

	for _, candidate := range legacyUserIDCandidates(apiKey, userID) {
		err = s.getTaskByUserAndTaskID(ctx, candidate, taskID, &task)
		if err == nil {
			logger := s.loggerFromContext(ctx)
			logger.Info("rag task matched legacy user id",
				zap.String("user_id", userID),
				zap.String("legacy_user_id", candidate),
				zap.String("task_id", taskID),
			)
			return &task, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, errors.Wrap(err, "query rag task by legacy user id")
		}
	}

	task = Task{UserID: userID, TaskID: taskID, CreatedAt: s.clock(), UpdatedAt: s.clock()}
	if err := s.insertTask(ctx, &task); err != nil {
		return nil, errors.Wrap(err, "create rag task")
	}
	logger := s.loggerFromContext(ctx)
	logger.Info("rag task created", zap.String("user_id", userID), zap.String("task_id", taskID))
	return &task, nil
}

// legacyUserIDCandidates returns possible pre-migration user IDs for compatibility lookups.
func legacyUserIDCandidates(apiKey string, canonicalUserID string) []string {
	legacy := legacyUserIDFromAPIKey(apiKey)
	if legacy == "" || legacy == canonicalUserID {
		return nil
	}

	return []string{legacy}
}

// legacyUserIDFromAPIKey reproduces the historical RAG user ID algorithm.
func legacyUserIDFromAPIKey(apiKey string) string {
	token := strings.TrimSpace(apiKey)
	if token == "" {
		return ""
	}

	hash := sha256.Sum256([]byte(token))
	hashPrefix := hex.EncodeToString(hash[:])
	keyPrefix := token
	if len(keyPrefix) > 7 {
		keyPrefix = keyPrefix[:7]
	}

	if len(hashPrefix) < 7 {
		return ""
	}

	return keyPrefix + "_" + hashPrefix[:7]
}

func (s *Service) ensureChunks(ctx context.Context, task *Task, input ExtractInput) error {
	fragments := s.chunker.Split(input.Materials, s.settings.MaxChunkChars)
	if len(fragments) == 0 {
		return errors.New("no chunks generated from materials")
	}

	hash := sha256.Sum256([]byte(strings.Join(cleanedFragments(fragments), "\n")))
	materialsHash := hex.EncodeToString(hash[:])

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Wrap(err, "begin rag ingestion tx")
	}
	defer func() {
		_ = tx.Rollback()
	}()

	countQuery := rebindPlaceholders(s.dialect, `SELECT COUNT(1) FROM mcp_rag_chunks WHERE task_id = ? AND materials_hash = ?`)
	var count int64
	if err := tx.QueryRowContext(ctx, countQuery, task.ID, materialsHash).Scan(&count); err != nil {
		return errors.Wrap(err, "check existing chunks")
	}
	if count > 0 {
		if commitErr := tx.Commit(); commitErr != nil {
			return errors.Wrap(commitErr, "commit rag ingestion tx")
		}
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
		metadataBytes, marshalErr := json.Marshal(map[string]string{
			"user_id": task.UserID,
			"task_id": task.TaskID,
			"hash":    materialsHash,
		})
		if marshalErr != nil {
			return errors.Wrap(marshalErr, "encode chunk metadata")
		}

		chunk := Chunk{
			TaskID:        task.ID,
			MaterialsHash: materialsHash,
			ChunkIndex:    fragment.Index,
			Text:          fragment.Text,
			CleanedText:   fragment.Cleaned,
			Metadata:      metadataBytes,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if err := s.insertChunkTx(ctx, tx, &chunk); err != nil {
			return errors.Wrap(err, "insert chunk")
		}

		if err := s.insertEmbeddingTx(ctx, tx, Embedding{
			ChunkID:   chunk.ID,
			Vector:    embeddings[idx],
			Model:     s.settings.EmbeddingModel,
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			return errors.Wrap(err, "insert embedding")
		}

		tokensJSON, marshalErr := json.Marshal(fragment.Tokens)
		if marshalErr != nil {
			return errors.Wrap(marshalErr, "encode tokens")
		}
		if err := s.insertBM25Tx(ctx, tx, BM25Row{
			ChunkID:    chunk.ID,
			Tokens:     tokensJSON,
			TokenCount: len(fragment.Tokens),
			Tokenizer:  "builtin",
			CreatedAt:  now,
			UpdatedAt:  now,
		}); err != nil {
			return errors.Wrap(err, "insert bm25 row")
		}
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return errors.Wrap(commitErr, "commit rag ingestion tx")
	}

	logger := s.loggerFromContext(ctx)
	logger.Info("rag materials ingested",
		zap.String("user_id", task.UserID),
		zap.String("task_id", task.TaskID),
		zap.Int("chunks", len(fragments)),
	)

	return nil
}

func (s *Service) fetchCandidates(ctx context.Context, taskID int64, queryVec pgvector.Vector, limit int) ([]candidateChunk, error) {
	logger := s.loggerFromContext(ctx)
	logger.Debug("fetching rag candidates", zap.Int64("task_id", taskID), zap.Int("limit", limit))
	rows := make([]candidateChunk, 0, limit)
	query := rebindPlaceholders(s.dialect, `
		    SELECT c.id, c.text, c.cleaned_text, e.vector AS embedding, b.tokens
            FROM mcp_rag_chunks c
            JOIN mcp_rag_embeddings e ON e.chunk_id = c.id
            LEFT JOIN mcp_rag_bm25 b ON b.chunk_id = c.id
            WHERE c.task_id = ?
		    ORDER BY e.vector <=> ? ASC
            LIMIT ?
	        `)

	queryVectorArg, err := vectorToDBValue(queryVec)
	if err != nil {
		return nil, errors.Wrap(err, "encode query vector")
	}

	dbRows, err := s.db.QueryContext(ctx, query, taskID, queryVectorArg, limit)
	if err != nil {
		return nil, errors.Wrap(err, "query rag candidates")
	}
	defer dbRows.Close()

	for dbRows.Next() {
		var row candidateChunk
		var embeddingRaw any
		var tokensRaw any
		if scanErr := dbRows.Scan(&row.ChunkID, &row.Text, &row.Cleaned, &embeddingRaw, &tokensRaw); scanErr != nil {
			return nil, errors.Wrap(scanErr, "scan rag candidate")
		}
		if convErr := scanVectorValue(embeddingRaw, &row.Embedding); convErr != nil {
			return nil, errors.Wrap(convErr, "decode candidate embedding")
		}
		row.TokenBytes = toJSONBytes(tokensRaw)
		rows = append(rows, row)
	}
	if rowsErr := dbRows.Err(); rowsErr != nil {
		return nil, errors.Wrap(rowsErr, "iterate rag candidates")
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
	ChunkID    int64
	Text       string
	Cleaned    string
	Embedding  pgvector.Vector
	TokenBytes []byte
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

// sqlDialect represents the SQL placeholder style and DDL variant in use.
type sqlDialect int

const (
	sqlDialectUnknown sqlDialect = iota
	sqlDialectPostgres
	sqlDialectSQLite
)

// detectSQLDialect infers the SQL dialect from the active driver implementation.
func detectSQLDialect(db *sql.DB) sqlDialect {
	if db == nil {
		return sqlDialectUnknown
	}

	return detectSQLDialectByDriverType(fmt.Sprintf("%T", db.Driver()))
}

// detectSQLDialectByDriverType infers SQL dialect from a concrete driver type name.
func detectSQLDialectByDriverType(driverType string) sqlDialect {
	t := strings.ToLower(driverType)

	if strings.Contains(t, "sqlite") {
		return sqlDialectSQLite
	}
	if strings.Contains(t, "postgres") ||
		strings.Contains(t, "pgx") ||
		strings.Contains(t, "pq") ||
		strings.Contains(t, "sqlmock") ||
		strings.Contains(t, "stdlib.driver") {
		return sqlDialectPostgres
	}

	return sqlDialectUnknown
}

// String returns the human-readable sql dialect name.
func (d sqlDialect) String() string {
	switch d {
	case sqlDialectPostgres:
		return "postgres"
	case sqlDialectSQLite:
		return "sqlite"
	default:
		return "unknown"
	}
}

// rebindPlaceholders rewrites '?' placeholders to PostgreSQL-style positional parameters.
func rebindPlaceholders(dialect sqlDialect, query string) string {
	if dialect != sqlDialectPostgres {
		return query
	}

	index := 1
	var builder strings.Builder
	builder.Grow(len(query) + 16)
	for _, ch := range query {
		if ch == '?' {
			builder.WriteString(fmt.Sprintf("$%d", index))
			index++
			continue
		}
		builder.WriteRune(ch)
	}

	return builder.String()
}

// getTaskByUserAndTaskID retrieves one task row by user/task tuple.
func (s *Service) getTaskByUserAndTaskID(ctx context.Context, userID, taskID string, out *Task) error {
	query := rebindPlaceholders(s.dialect, `
		SELECT id, user_id, task_id, created_at, updated_at
		FROM mcp_rag_tasks
		WHERE user_id = ? AND task_id = ?
		LIMIT 1
	`)

	return s.db.QueryRowContext(ctx, query, userID, taskID).Scan(
		&out.ID,
		&out.UserID,
		&out.TaskID,
		&out.CreatedAt,
		&out.UpdatedAt,
	)
}

// insertTask inserts a task and sets its generated identifier.
func (s *Service) insertTask(ctx context.Context, task *Task) error {
	if s.dialect == sqlDialectPostgres {
		query := rebindPlaceholders(s.dialect, `
			INSERT INTO mcp_rag_tasks(user_id, task_id, created_at, updated_at)
			VALUES(?, ?, ?, ?)
			RETURNING id
		`)
		return s.db.QueryRowContext(ctx, query, task.UserID, task.TaskID, task.CreatedAt, task.UpdatedAt).Scan(&task.ID)
	}

	query := rebindPlaceholders(s.dialect, `
		INSERT INTO mcp_rag_tasks(user_id, task_id, created_at, updated_at)
		VALUES(?, ?, ?, ?)
	`)
	result, err := s.db.ExecContext(ctx, query, task.UserID, task.TaskID, task.CreatedAt, task.UpdatedAt)
	if err != nil {
		return errors.WithStack(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return errors.WithStack(err)
	}
	task.ID = id
	return nil
}

// insertChunkTx inserts one chunk row in the active transaction and sets its ID.
func (s *Service) insertChunkTx(ctx context.Context, tx *sql.Tx, chunk *Chunk) error {
	if s.dialect == sqlDialectPostgres {
		query := rebindPlaceholders(s.dialect, `
			INSERT INTO mcp_rag_chunks(task_id, materials_hash, chunk_index, text, cleaned_text, metadata, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)
			RETURNING id
		`)
		return tx.QueryRowContext(
			ctx,
			query,
			chunk.TaskID,
			chunk.MaterialsHash,
			chunk.ChunkIndex,
			chunk.Text,
			chunk.CleanedText,
			chunk.Metadata,
			chunk.CreatedAt,
			chunk.UpdatedAt,
		).Scan(&chunk.ID)
	}

	query := rebindPlaceholders(s.dialect, `
		INSERT INTO mcp_rag_chunks(task_id, materials_hash, chunk_index, text, cleaned_text, metadata, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
	`)
	result, err := tx.ExecContext(
		ctx,
		query,
		chunk.TaskID,
		chunk.MaterialsHash,
		chunk.ChunkIndex,
		chunk.Text,
		chunk.CleanedText,
		chunk.Metadata,
		chunk.CreatedAt,
		chunk.UpdatedAt,
	)
	if err != nil {
		return errors.WithStack(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return errors.WithStack(err)
	}
	chunk.ID = id
	return nil
}

// insertEmbeddingTx inserts one embedding row for a chunk.
func (s *Service) insertEmbeddingTx(ctx context.Context, tx *sql.Tx, embedding Embedding) error {
	vectorValue, err := vectorToDBValue(embedding.Vector)
	if err != nil {
		return errors.Wrap(err, "encode embedding vector")
	}

	query := rebindPlaceholders(s.dialect, `
		INSERT INTO mcp_rag_embeddings(chunk_id, vector, model, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?)
	`)
	if _, err := tx.ExecContext(
		ctx,
		query,
		embedding.ChunkID,
		vectorValue,
		embedding.Model,
		embedding.CreatedAt,
		embedding.UpdatedAt,
	); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// insertBM25Tx inserts one lexical token row for a chunk.
func (s *Service) insertBM25Tx(ctx context.Context, tx *sql.Tx, row BM25Row) error {
	query := rebindPlaceholders(s.dialect, `
		INSERT INTO mcp_rag_bm25(chunk_id, tokens, token_count, tokenizer, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)
	`)
	if _, err := tx.ExecContext(
		ctx,
		query,
		row.ChunkID,
		row.Tokens,
		row.TokenCount,
		row.Tokenizer,
		row.CreatedAt,
		row.UpdatedAt,
	); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// vectorToDBValue converts a vector into the driver value used in SQL parameters.
func vectorToDBValue(vector pgvector.Vector) (any, error) {
	value, err := vector.Value()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return value, nil
}

// scanVectorValue decodes a scanned SQL value into a vector.
func scanVectorValue(raw any, vector *pgvector.Vector) error {
	if raw == nil {
		*vector = pgvector.Vector{}
		return nil
	}

	if err := vector.Scan(raw); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// toJSONBytes normalizes a scanned SQL value to JSON bytes.
func toJSONBytes(raw any) []byte {
	switch value := raw.(type) {
	case nil:
		return nil
	case []byte:
		return value
	case string:
		return []byte(value)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil
		}
		return encoded
	}
}
