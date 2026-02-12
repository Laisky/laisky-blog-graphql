package files

import (
	"time"

	"github.com/pgvector/pgvector-go"
)

// FileChunk stores a chunk extracted from a file for search.
type FileChunk struct {
	ID          int64      `gorm:"primaryKey"`
	APIKeyHash  string     `gorm:"column:apikey_hash;size:64;not null"`
	Project     string     `gorm:"size:128;not null"`
	FilePath    string     `gorm:"column:file_path;size:1024;not null"`
	ChunkIndex  int        `gorm:"not null"`
	StartByte   int64      `gorm:"column:start_byte;not null"`
	EndByte     int64      `gorm:"column:end_byte;not null"`
	Content     string     `gorm:"column:chunk_content;type:text;not null"`
	ContentHash string     `gorm:"column:content_hash;size:64;not null"`
	CreatedAt   time.Time  `gorm:"not null"`
	UpdatedAt   time.Time  `gorm:"not null"`
	LastServed  *time.Time `gorm:"column:last_served_at"`
}

// TableName returns the database table name.
func (FileChunk) TableName() string {
	return "mcp_file_chunks"
}

// FileChunkEmbedding stores vector embeddings for a chunk.
type FileChunkEmbedding struct {
	ChunkID   int64           `gorm:"column:chunk_id;primaryKey"`
	Embedding pgvector.Vector `gorm:"type:vector(1536);not null"`
	Model     string          `gorm:"size:128;not null"`
	CreatedAt time.Time       `gorm:"not null"`
	UpdatedAt time.Time       `gorm:"not null"`
}

// TableName returns the database table name.
func (FileChunkEmbedding) TableName() string {
	return "mcp_file_chunk_embeddings"
}

// FileChunkBM25 stores lexical tokens for a chunk.
type FileChunkBM25 struct {
	ChunkID    int64     `gorm:"column:chunk_id;primaryKey"`
	Tokens     []byte    `gorm:"type:jsonb;not null"`
	TokenCount int       `gorm:"column:token_count;not null"`
	Tokenizer  string    `gorm:"size:64;not null"`
	CreatedAt  time.Time `gorm:"not null"`
	UpdatedAt  time.Time `gorm:"not null"`
}

// TableName returns the database table name.
func (FileChunkBM25) TableName() string {
	return "mcp_file_chunk_bm25"
}

// FileIndexJob captures pending indexing operations.
type FileIndexJob struct {
	ID            int64      `gorm:"primaryKey"`
	APIKeyHash    string     `gorm:"column:apikey_hash;size:64;not null"`
	Project       string     `gorm:"size:128;not null"`
	FilePath      string     `gorm:"column:file_path;size:1024;not null"`
	Operation     string     `gorm:"size:16;not null"`
	FileUpdatedAt *time.Time `gorm:"column:file_updated_at"`
	Status        string     `gorm:"size:16;not null"`
	RetryCount    int        `gorm:"column:retry_count;not null"`
	AvailableAt   time.Time  `gorm:"column:available_at;not null"`
	CreatedAt     time.Time  `gorm:"not null"`
	UpdatedAt     time.Time  `gorm:"not null"`
}

// TableName returns the database table name.
func (FileIndexJob) TableName() string {
	return "mcp_file_index_jobs"
}
