package files

import (
	"time"

	"github.com/pgvector/pgvector-go"
)

// FileChunk stores a chunk extracted from a file for search.
type FileChunk struct {
	ID          int64
	APIKeyHash  string
	Project     string
	FilePath    string
	ChunkIndex  int
	StartByte   int64
	EndByte     int64
	FileSize    int64
	Content     string
	ContentHash string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	LastServed  *time.Time
	// FileContentHash binds the chunk to a whole-file content generation. It is
	// distinct from ContentHash, which hashes only this chunk's own text (§5.1).
	FileContentHash string
	// FileSummary, SummaryContentHash, and SummaryStatus are transient join fields
	// carried through search so a hit can pair its chunk with the matching summary.
	FileSummary        string
	SummaryContentHash string
	SummaryStatus      string
}

// TableName returns the database table name.
func (FileChunk) TableName() string {
	return "mcp_file_chunks"
}

// FileChunkEmbedding stores vector embeddings for a chunk.
type FileChunkEmbedding struct {
	ChunkID   int64
	Embedding pgvector.Vector
	Model     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName returns the database table name.
func (FileChunkEmbedding) TableName() string {
	return "mcp_file_chunk_embeddings"
}

// FileChunkBM25 stores lexical tokens for a chunk.
type FileChunkBM25 struct {
	ChunkID    int64
	Tokens     []byte
	TokenCount int
	Tokenizer  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// TableName returns the database table name.
func (FileChunkBM25) TableName() string {
	return "mcp_file_chunk_bm25"
}

// FileIndexJob captures pending indexing operations.
type FileIndexJob struct {
	ID            int64
	APIKeyHash    string
	Project       string
	FilePath      string
	Operation     string
	FileUpdatedAt *time.Time
	Status        string
	RetryCount    int
	AvailableAt   time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
	// ContentHash is the whole-file content generation the job expects. A job whose
	// expected hash no longer matches the active file is skipped before any model
	// call so rapid successive writes coalesce (§4.1, §4.2).
	ContentHash string
	// LastErrorCode is a safe machine code for a SUMMARY_REFRESH job (§4.6).
	LastErrorCode string
	// SummaryGenerationKey deduplicates SUMMARY_REFRESH jobs (§4.6).
	SummaryGenerationKey string
}

// TableName returns the database table name.
func (FileIndexJob) TableName() string {
	return "mcp_file_index_jobs"
}
