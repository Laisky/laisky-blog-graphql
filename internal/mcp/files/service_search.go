package files

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"

	errors "github.com/Laisky/errors/v2"
	"github.com/Laisky/zap"
	"github.com/pgvector/pgvector-go"
)

type searchCandidate struct {
	Chunk         FileChunk
	SemanticScore float64
	LexicalScore  float64
	FinalScore    float64
}

// Search performs hybrid retrieval over indexed file chunks.
func (s *Service) Search(ctx context.Context, auth AuthContext, project, query, pathPrefix string, limit int) (SearchResult, error) {
	if err := s.validateAuth(auth); err != nil {
		return SearchResult{}, errors.WithStack(err)
	}
	if err := ValidateProject(project); err != nil {
		return SearchResult{}, errors.WithStack(err)
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchResult{}, errors.WithStack(NewError(ErrCodeInvalidQuery, "query cannot be empty", false))
	}
	if limit <= 0 {
		limit = s.settings.Search.LimitDefault
	}
	if limit > s.settings.Search.LimitMax {
		limit = s.settings.Search.LimitMax
	}
	if !s.settings.Search.Enabled {
		return SearchResult{}, errors.WithStack(NewError(ErrCodeSearchBackend, "search disabled", false))
	}

	lexical, lexicalErr := s.fetchLexicalCandidates(ctx, auth.APIKeyHash, project, pathPrefix, query, s.settings.Search.LexicalCandidates)
	if lexicalErr != nil {
		s.LoggerFromContext(ctx).Debug("file search lexical retrieval failed",
			zap.String("project", project),
			zap.String("path_prefix", pathPrefix),
			zap.Int("candidate_limit", s.settings.Search.LexicalCandidates),
			zap.Error(lexicalErr),
		)
	}

	semantic := []searchCandidate{}
	var semanticErr error
	if s.embedder == nil {
		semanticErr = NewError(ErrCodeSearchBackend, "embedder not configured", false)
		s.LoggerFromContext(ctx).Debug("file search semantic retrieval skipped: embedder not configured",
			zap.String("project", project),
			zap.String("path_prefix", pathPrefix),
		)
	} else {
		vectors, embedErr := s.embedder.EmbedTexts(ctx, auth.APIKey, []string{query})
		if embedErr != nil {
			semanticErr = errors.Wrap(embedErr, "embed query")
			s.LoggerFromContext(ctx).Debug("file search semantic embedding failed",
				zap.String("project", project),
				zap.String("path_prefix", pathPrefix),
				zap.Error(embedErr),
			)
		} else if len(vectors) == 0 {
			semanticErr = NewError(ErrCodeSearchBackend, "embed query returned no vectors", true)
			s.LoggerFromContext(ctx).Debug("file search semantic embedding returned no vectors",
				zap.String("project", project),
				zap.String("path_prefix", pathPrefix),
			)
		} else {
			queryVec := vectors[0]
			semantic, semanticErr = s.fetchSemanticCandidates(ctx, auth.APIKeyHash, project, pathPrefix, queryVec, s.settings.Search.VectorCandidates)
			if semanticErr != nil {
				s.LoggerFromContext(ctx).Debug("file search semantic retrieval failed",
					zap.String("project", project),
					zap.String("path_prefix", pathPrefix),
					zap.Int("candidate_limit", s.settings.Search.VectorCandidates),
					zap.Error(semanticErr),
				)
			}
		}
	}

	if lexicalErr != nil && semanticErr != nil {
		return SearchResult{}, errors.WithStack(NewError(ErrCodeSearchBackend, "search backends unavailable", true))
	}

	merged := mergeCandidates(semantic, lexical)
	if len(merged) == 0 {
		s.logEmptySearchDiagnostics(ctx, auth.APIKeyHash, project, pathPrefix, lexicalErr, semanticErr)
		return SearchResult{Chunks: nil}, nil
	}

	finalCandidates := merged
	if s.rerank != nil {
		if reranked, rerankErr := s.applyRerank(ctx, auth.APIKey, query, merged); rerankErr == nil {
			finalCandidates = reranked
		} else {
			finalCandidates = applyFallbackScores(merged, s.settings.Search.SemanticWeight, s.settings.Search.LexicalWeight)
		}
	} else {
		finalCandidates = applyFallbackScores(merged, s.settings.Search.SemanticWeight, s.settings.Search.LexicalWeight)
	}

	sort.Slice(finalCandidates, func(i, j int) bool {
		return finalCandidates[i].FinalScore > finalCandidates[j].FinalScore
	})

	if len(finalCandidates) > limit {
		finalCandidates = finalCandidates[:limit]
	}

	chunkIDs := make([]int64, 0, len(finalCandidates))
	chunks := make([]ChunkEntry, 0, len(finalCandidates))
	for _, c := range finalCandidates {
		chunkIDs = append(chunkIDs, c.Chunk.ID)
		chunks = append(chunks, ChunkEntry{
			FilePath:           c.Chunk.FilePath,
			FileSeekStartBytes: c.Chunk.StartByte,
			FileSeekEndBytes:   c.Chunk.EndByte,
			ChunkContent:       c.Chunk.Content,
			Score:              c.FinalScore,
		})
	}

	if err := s.updateLastServed(ctx, chunkIDs); err != nil {
		return SearchResult{}, errors.WithStack(err)
	}

	return SearchResult{Chunks: chunks}, nil
}

// logEmptySearchDiagnostics emits DEBUG diagnostics for empty search results.
func (s *Service) logEmptySearchDiagnostics(ctx context.Context, apiKeyHash, project, pathPrefix string, lexicalErr, semanticErr error) {
	chunkCount := s.countRowsForSearch(ctx, "mcp_file_chunks c", apiKeyHash, project, pathPrefix)
	embeddingCount := s.countRowsForSearch(ctx, "mcp_file_chunk_embeddings e JOIN mcp_file_chunks c ON c.id = e.chunk_id", apiKeyHash, project, pathPrefix)
	pendingJobs := s.countPendingIndexJobs(ctx, apiKeyHash, project)

	logger := s.LoggerFromContext(ctx)
	logger.Debug("file search returned no candidates",
		zap.String("project", project),
		zap.String("path_prefix", pathPrefix),
		zap.Int("lexical_candidates", 0),
		zap.Int("semantic_candidates", 0),
		zap.Int64("indexed_chunk_count", chunkCount),
		zap.Int64("indexed_embedding_count", embeddingCount),
		zap.Int64("pending_index_jobs", pendingJobs),
		zap.Bool("lexical_error", lexicalErr != nil),
		zap.Bool("semantic_error", semanticErr != nil),
	)
}

// countRowsForSearch counts indexed rows by tenant/project with optional path prefix filtering.
func (s *Service) countRowsForSearch(ctx context.Context, source, apiKeyHash, project, pathPrefix string) int64 {
	where := "c.apikey_hash = ? AND c.project = ?"
	args := []any{apiKeyHash, project}
	if strings.TrimSpace(pathPrefix) != "" {
		where += " AND c.file_path LIKE ?"
		args = append(args, pathPrefix+"%")
	}

	query := "SELECT COUNT(1) FROM " + source + " WHERE " + where
	var count int64
	if err := s.db.WithContext(ctx).Raw(query, args...).Scan(&count).Error; err != nil {
		return 0
	}
	return count
}

// countPendingIndexJobs returns pending or processing index jobs for diagnostics.
func (s *Service) countPendingIndexJobs(ctx context.Context, apiKeyHash, project string) int64 {
	var count int64
	err := s.db.WithContext(ctx).Raw(
		"SELECT COUNT(1) FROM mcp_file_index_jobs WHERE apikey_hash = ? AND project = ? AND status IN (?, ?)",
		apiKeyHash,
		project,
		"pending",
		"processing",
	).Scan(&count).Error
	if err != nil {
		return 0
	}
	return count
}

// fetchSemanticCandidates retrieves semantic candidates from the best backend.
func (s *Service) fetchSemanticCandidates(ctx context.Context, apiKeyHash, project, pathPrefix string, queryVec pgvector.Vector, limit int) ([]searchCandidate, error) {
	if limit <= 0 {
		return nil, nil
	}
	if isPostgresDialect(s.db) {
		return s.fetchSemanticCandidatesPostgres(ctx, apiKeyHash, project, pathPrefix, queryVec, limit)
	}
	return s.fetchSemanticCandidatesInMemory(ctx, apiKeyHash, project, pathPrefix, queryVec, limit)
}

// fetchSemanticCandidatesPostgres performs vector distance search via pgvector.
func (s *Service) fetchSemanticCandidatesPostgres(ctx context.Context, apiKeyHash, project, pathPrefix string, queryVec pgvector.Vector, limit int) ([]searchCandidate, error) {
	args := []any{queryVec, apiKeyHash, project}
	query := `SELECT c.id, c.file_path, c.start_byte, c.end_byte, c.chunk_content, e.embedding <-> ? AS distance
		FROM mcp_file_chunk_embeddings e
		JOIN mcp_file_chunks c ON c.id = e.chunk_id
		JOIN mcp_files f ON f.apikey_hash = c.apikey_hash AND f.project = c.project AND f.path = c.file_path AND f.deleted = FALSE
		WHERE c.apikey_hash = ? AND c.project = ?`
	if strings.TrimSpace(pathPrefix) != "" {
		query += " AND c.file_path LIKE ?"
		args = append(args, pathPrefix+"%")
	}
	query += " ORDER BY e.embedding <-> ? LIMIT ?"
	args = append(args, queryVec, limit)

	type row struct {
		ID        int64
		FilePath  string
		StartByte int64
		EndByte   int64
		Content   string
		Distance  float64
	}
	rows := make([]row, 0, limit)
	if err := s.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, errors.Wrap(err, "query semantic candidates")
	}

	result := make([]searchCandidate, 0, len(rows))
	for _, r := range rows {
		score := 1.0 / (1.0 + r.Distance)
		result = append(result, searchCandidate{
			Chunk: FileChunk{
				ID:        r.ID,
				FilePath:  r.FilePath,
				StartByte: r.StartByte,
				EndByte:   r.EndByte,
				Content:   r.Content,
			},
			SemanticScore: score,
		})
	}
	return result, nil
}

// fetchSemanticCandidatesInMemory computes similarity scores without pgvector.
func (s *Service) fetchSemanticCandidatesInMemory(ctx context.Context, apiKeyHash, project, pathPrefix string, queryVec pgvector.Vector, limit int) ([]searchCandidate, error) {
	rows, err := s.fetchChunkEmbeddings(ctx, apiKeyHash, project, pathPrefix)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	candidates := make([]searchCandidate, 0, len(rows))
	querySlice := queryVec.Slice()
	for _, row := range rows {
		score := cosineSimilarity(querySlice, row.Embedding)
		candidates = append(candidates, searchCandidate{
			Chunk: FileChunk{
				ID:        row.ChunkID,
				FilePath:  row.FilePath,
				StartByte: row.StartByte,
				EndByte:   row.EndByte,
				Content:   row.Content,
			},
			SemanticScore: score,
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].SemanticScore > candidates[j].SemanticScore
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

// fetchLexicalCandidates routes lexical retrieval to the best available backend.
func (s *Service) fetchLexicalCandidates(ctx context.Context, apiKeyHash, project, pathPrefix, query string, limit int) ([]searchCandidate, error) {
	if limit <= 0 {
		return nil, nil
	}
	if isPostgresDialect(s.db) {
		return s.fetchLexicalCandidatesPostgres(ctx, apiKeyHash, project, pathPrefix, query, limit)
	}
	return s.fetchLexicalCandidatesInMemory(ctx, apiKeyHash, project, pathPrefix, query, limit)
}

// fetchLexicalCandidatesPostgres uses tsvector ranking to score candidates.
func (s *Service) fetchLexicalCandidatesPostgres(ctx context.Context, apiKeyHash, project, pathPrefix, query string, limit int) ([]searchCandidate, error) {
	args := []any{query, apiKeyHash, project}
	statement := `SELECT c.id, c.file_path, c.start_byte, c.end_byte, c.chunk_content,
		ts_rank_cd(to_tsvector('simple', c.chunk_content), plainto_tsquery('simple', ?)) AS score
		FROM mcp_file_chunks c
		JOIN mcp_files f ON f.apikey_hash = c.apikey_hash AND f.project = c.project AND f.path = c.file_path AND f.deleted = FALSE
		WHERE c.apikey_hash = ? AND c.project = ?`
	if strings.TrimSpace(pathPrefix) != "" {
		statement += " AND c.file_path LIKE ?"
		args = append(args, pathPrefix+"%")
	}
	statement += " ORDER BY score DESC LIMIT ?"
	args = append(args, limit)

	type row struct {
		ID        int64
		FilePath  string
		StartByte int64
		EndByte   int64
		Content   string
		Score     float64
	}
	rows := make([]row, 0, limit)
	if err := s.db.WithContext(ctx).Raw(statement, args...).Scan(&rows).Error; err != nil {
		return nil, errors.Wrap(err, "query lexical candidates")
	}

	result := make([]searchCandidate, 0, len(rows))
	for _, r := range rows {
		result = append(result, searchCandidate{
			Chunk: FileChunk{
				ID:        r.ID,
				FilePath:  r.FilePath,
				StartByte: r.StartByte,
				EndByte:   r.EndByte,
				Content:   r.Content,
			},
			LexicalScore: r.Score,
		})
	}
	return result, nil
}

// fetchLexicalCandidatesInMemory computes lexical scores in-process.
func (s *Service) fetchLexicalCandidatesInMemory(ctx context.Context, apiKeyHash, project, pathPrefix, query string, limit int) ([]searchCandidate, error) {
	chunks, err := s.fetchChunkRows(ctx, apiKeyHash, project, pathPrefix)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	queryTokens := tokenize(query)
	candidates := make([]searchCandidate, 0, len(chunks))
	for _, row := range chunks {
		score := lexicalScore(queryTokens, tokenize(row.Content))
		if score == 0 {
			continue
		}
		candidates = append(candidates, searchCandidate{
			Chunk: FileChunk{
				ID:        row.ID,
				FilePath:  row.FilePath,
				StartByte: row.StartByte,
				EndByte:   row.EndByte,
				Content:   row.Content,
			},
			LexicalScore: score,
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].LexicalScore > candidates[j].LexicalScore
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

// mergeCandidates deduplicates semantic and lexical candidates by chunk ID.
func mergeCandidates(semantic, lexical []searchCandidate) []searchCandidate {
	merged := make(map[int64]searchCandidate)
	for _, c := range semantic {
		merged[c.Chunk.ID] = c
	}
	for _, c := range lexical {
		item := merged[c.Chunk.ID]
		item.Chunk = c.Chunk
		item.LexicalScore = c.LexicalScore
		if item.SemanticScore == 0 {
			item.SemanticScore = c.SemanticScore
		}
		merged[c.Chunk.ID] = item
	}

	result := make([]searchCandidate, 0, len(merged))
	for _, c := range merged {
		result = append(result, c)
	}
	return result
}

// applyRerank calls the external rerank model to compute final scores.
func (s *Service) applyRerank(ctx context.Context, apiKey, query string, candidates []searchCandidate) ([]searchCandidate, error) {
	docs := make([]string, 0, len(candidates))
	for _, c := range candidates {
		docs = append(docs, c.Chunk.Content)
	}
	scores, err := s.rerank.Rerank(ctx, apiKey, query, docs)
	if err != nil {
		return nil, err
	}
	if len(scores) != len(candidates) {
		return nil, errors.New("rerank response length mismatch")
	}
	for i := range candidates {
		candidates[i].FinalScore = scores[i]
	}
	return candidates, nil
}

// applyFallbackScores computes fused scores when reranking is unavailable.
func applyFallbackScores(candidates []searchCandidate, semanticWeight, lexicalWeight float64) []searchCandidate {
	semanticScores := make([]float64, 0, len(candidates))
	lexicalScores := make([]float64, 0, len(candidates))
	for _, c := range candidates {
		semanticScores = append(semanticScores, c.SemanticScore)
		lexicalScores = append(lexicalScores, c.LexicalScore)
	}

	semanticMin, semanticMax := minMax(semanticScores)
	lexicalMin, lexicalMax := minMax(lexicalScores)

	for i := range candidates {
		semanticNorm := normalizeScore(candidates[i].SemanticScore, semanticMin, semanticMax)
		lexicalNorm := normalizeScore(candidates[i].LexicalScore, lexicalMin, lexicalMax)
		candidates[i].FinalScore = semanticWeight*semanticNorm + lexicalWeight*lexicalNorm
	}
	return candidates
}

// updateLastServed updates last_served_at for returned chunk IDs.
func (s *Service) updateLastServed(ctx context.Context, chunkIDs []int64) error {
	if len(chunkIDs) == 0 {
		return nil
	}
	now := s.clock()
	return s.db.WithContext(ctx).Model(&FileChunk{}).
		Where("id IN ?", chunkIDs).
		Update("last_served_at", now).Error
}

type chunkEmbeddingRow struct {
	ChunkID   int64
	FilePath  string
	StartByte int64
	EndByte   int64
	Content   string
	Embedding []float32
}

// fetchChunkEmbeddings loads chunks and embeddings for in-memory similarity.
func (s *Service) fetchChunkEmbeddings(ctx context.Context, apiKeyHash, project, pathPrefix string) ([]chunkEmbeddingRow, error) {
	args := []any{apiKeyHash, project}
	query := `SELECT c.id, c.file_path, c.start_byte, c.end_byte, c.chunk_content, e.embedding
		FROM mcp_file_chunk_embeddings e
		JOIN mcp_file_chunks c ON c.id = e.chunk_id
		JOIN mcp_files f ON f.apikey_hash = c.apikey_hash AND f.project = c.project AND f.path = c.file_path AND f.deleted = FALSE
		WHERE c.apikey_hash = ? AND c.project = ?`
	if strings.TrimSpace(pathPrefix) != "" {
		query += " AND c.file_path LIKE ?"
		args = append(args, pathPrefix+"%")
	}

	rows, err := s.db.WithContext(ctx).Raw(query, args...).Rows()
	if err != nil {
		return nil, errors.Wrap(err, "query chunk embeddings")
	}
	defer rows.Close()

	var results []chunkEmbeddingRow
	for rows.Next() {
		var row chunkEmbeddingRow
		var raw any
		if scanErr := rows.Scan(&row.ChunkID, &row.FilePath, &row.StartByte, &row.EndByte, &row.Content, &raw); scanErr != nil {
			return nil, errors.Wrap(scanErr, "scan chunk embedding")
		}
		emb, err := decodeEmbedding(raw)
		if err != nil {
			return nil, errors.Wrap(err, "decode embedding")
		}
		row.Embedding = emb
		results = append(results, row)
	}
	return results, nil
}

// decodeEmbedding parses embedding payloads from database scans.
func decodeEmbedding(raw any) ([]float32, error) {
	switch v := raw.(type) {
	case pgvector.Vector:
		return v.Slice(), nil
	case []byte:
		return parseEmbeddingJSON(v)
	case string:
		return parseEmbeddingJSON([]byte(v))
	default:
		return nil, errors.New("unsupported embedding format")
	}
}

// parseEmbeddingJSON converts JSON-encoded floats into float32 slices.
func parseEmbeddingJSON(data []byte) ([]float32, error) {
	var floats []float64
	if err := json.Unmarshal(data, &floats); err != nil {
		return nil, err
	}
	result := make([]float32, len(floats))
	for i, f := range floats {
		result[i] = float32(f)
	}
	return result, nil
}

type chunkRow struct {
	ID        int64
	FilePath  string
	StartByte int64
	EndByte   int64
	Content   string
}

func (s *Service) fetchChunkRows(ctx context.Context, apiKeyHash, project, pathPrefix string) ([]chunkRow, error) {
	args := []any{apiKeyHash, project}
	query := `SELECT c.id, c.file_path, c.start_byte, c.end_byte, c.chunk_content
		FROM mcp_file_chunks c
		JOIN mcp_files f ON f.apikey_hash = c.apikey_hash AND f.project = c.project AND f.path = c.file_path AND f.deleted = FALSE
		WHERE c.apikey_hash = ? AND c.project = ?`
	if strings.TrimSpace(pathPrefix) != "" {
		query += " AND c.file_path LIKE ?"
		args = append(args, pathPrefix+"%")
	}

	rows, err := s.db.WithContext(ctx).Raw(query, args...).Rows()
	if err != nil {
		return nil, errors.Wrap(err, "query chunks")
	}
	defer rows.Close()

	var result []chunkRow
	for rows.Next() {
		var row chunkRow
		if scanErr := rows.Scan(&row.ID, &row.FilePath, &row.StartByte, &row.EndByte, &row.Content); scanErr != nil {
			return nil, errors.Wrap(scanErr, "scan chunk row")
		}
		result = append(result, row)
	}
	return result, nil
}

// cosineSimilarity returns the cosine similarity between two vectors.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i] * b[i])
		normA += float64(a[i] * a[i])
		normB += float64(b[i] * b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// tokenize normalizes input text into lowercase tokens.
func tokenize(text string) []string {
	fields := strings.Fields(strings.ToLower(text))
	result := make([]string, 0, len(fields))
	for _, f := range fields {
		trimmed := strings.Trim(f, ".,;:!?()[]{}\"'`“”‘’")
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// lexicalScore computes a simple token overlap score.
func lexicalScore(queryTokens, docTokens []string) float64 {
	if len(queryTokens) == 0 || len(docTokens) == 0 {
		return 0
	}
	set := make(map[string]struct{}, len(docTokens))
	for _, t := range docTokens {
		set[t] = struct{}{}
	}
	var match int
	for _, t := range queryTokens {
		if _, ok := set[t]; ok {
			match++
		}
	}
	return float64(match)
}

// minMax returns the min and max values in a slice.
func minMax(values []float64) (float64, float64) {
	if len(values) == 0 {
		return 0, 0
	}
	minVal := values[0]
	maxVal := values[0]
	for _, v := range values[1:] {
		if v < minVal {
			minVal = v
		}
		if v > maxVal {
			maxVal = v
		}
	}
	return minVal, maxVal
}

// normalizeScore rescales a value to the [0,1] interval.
func normalizeScore(value, minVal, maxVal float64) float64 {
	if maxVal == minVal {
		return 0
	}
	return (value - minVal) / (maxVal - minVal)
}
