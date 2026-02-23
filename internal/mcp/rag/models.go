package rag

import (
	"time"

	pgvector "github.com/pgvector/pgvector-go"
)

// Task stores the per-user/task routing metadata used to isolate materials and embeddings.
type Task struct {
	ID        int64
	UserID    string
	TaskID    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Chunk represents a single cleaned text span that can be embedded and scored.
type Chunk struct {
	ID            int64
	TaskID        int64
	MaterialsHash string
	ChunkIndex    int
	Text          string
	CleanedText   string
	Metadata      []byte
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Embedding stores the vector representation for a chunk.
type Embedding struct {
	ChunkID   int64
	Vector    pgvector.Vector
	Model     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// BM25Row stores lexical statistics for a chunk to enable keyword-based scoring.
type BM25Row struct {
	ChunkID    int64
	Tokens     []byte
	TokenCount int
	Tokenizer  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

const (
	TableRAGTasks      = "mcp_rag_tasks"
	TableRAGChunks     = "mcp_rag_chunks"
	TableRAGEmbeddings = "mcp_rag_embeddings"
	TableRAGBM25       = "mcp_rag_bm25"
)
