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
}

// TableName returns the database table name.
func (FileIndexJob) TableName() string {
	return "mcp_file_index_jobs"
}
