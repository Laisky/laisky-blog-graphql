package files

import (
	"context"

	"github.com/Laisky/zap"
)

// buildContextualizedChunkInputs prepends LLM-generated chunk context to each chunk for indexing inputs.
func (s *Service) buildContextualizedChunkInputs(ctx context.Context, apiKey, document, filePath string, chunks []Chunk) []string {
	if len(chunks) == 0 {
		return nil
	}

	result := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		result = append(result, chunk.Content)
	}

	if s == nil || s.contextualizer == nil {
		return result
	}

	contexts, err := s.contextualizer.GenerateChunkContexts(ctx, apiKey, document, chunks)
	if err != nil {
		s.LoggerFromContext(ctx).Debug("generate chunk contexts failed, fallback to raw chunks",
			zap.String("file_path", filePath),
			zap.Int("chunk_count", len(chunks)),
			zap.Error(err),
		)
		return result
	}
	if len(contexts) != len(chunks) {
		s.LoggerFromContext(ctx).Debug("chunk context count mismatch, fallback to raw chunks",
			zap.String("file_path", filePath),
			zap.Int("chunk_count", len(chunks)),
			zap.Int("context_count", len(contexts)),
		)
		return result
	}

	for i, contextText := range contexts {
		if contextText == "" {
			continue
		}
		result[i] = contextText + "\n\n" + chunks[i].Content
	}

	return result
}
