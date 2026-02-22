package files

import "unicode/utf8"

// Chunk captures a slice of content with stable byte offsets.
type Chunk struct {
	Index     int
	StartByte int64
	EndByte   int64
	Content   string
}

// Chunker splits content into stable byte ranges.
type Chunker interface {
	Split(content string) []Chunk
}

// DefaultChunker splits content into byte-limited chunks.
type DefaultChunker struct {
	MaxBytes int
}

// Split divides content into chunks that preserve byte offsets.
func (c DefaultChunker) Split(content string) []Chunk {
	maxBytes := c.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 500
	}
	if content == "" {
		return nil
	}

	chunks := make([]Chunk, 0)
	start := 0
	index := 0
	for start < len(content) {
		end := start + maxBytes
		if end > len(content) {
			end = len(content)
		} else {
			for end < len(content) && !utf8.ValidString(content[start:end]) {
				end--
			}
			if end == start {
				end = min(start+maxBytes, len(content))
			}
		}

		chunks = append(chunks, Chunk{
			Index:     index,
			StartByte: int64(start),
			EndByte:   int64(end),
			Content:   content[start:end],
		})
		index++
		start = end
	}
	return chunks
}

// min returns the smaller of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
