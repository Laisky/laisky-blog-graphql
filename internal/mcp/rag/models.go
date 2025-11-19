package rag

import (
	"time"

	pgvector "github.com/pgvector/pgvector-go"
	"gorm.io/datatypes"
)

// Task stores the per-user/task routing metadata used to isolate materials and embeddings.
type Task struct {
	ID        int64     `gorm:"primaryKey"`
	UserID    string    `gorm:"size:128;not null;index:idx_rag_tasks_user_task,priority:1"`
	TaskID    string    `gorm:"size:128;not null;index:idx_rag_tasks_user_task,priority:2"`
	CreatedAt time.Time `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null"`
}

// Chunk represents a single cleaned text span that can be embedded and scored.
type Chunk struct {
	ID            int64             `gorm:"primaryKey"`
	TaskID        int64             `gorm:"not null;index:idx_rag_chunks_task_hash,priority:1"`
	MaterialsHash string            `gorm:"size:96;not null;index:idx_rag_chunks_task_hash,priority:2"`
	ChunkIndex    int               `gorm:"not null;index:idx_rag_chunks_unique,priority:3"`
	Text          string            `gorm:"type:text;not null"`
	CleanedText   string            `gorm:"type:text;not null"`
	Metadata      datatypes.JSONMap `gorm:"type:jsonb"`
	CreatedAt     time.Time         `gorm:"not null"`
	UpdatedAt     time.Time         `gorm:"not null"`
}

// Embedding stores the vector representation for a chunk.
type Embedding struct {
	ChunkID   int64           `gorm:"primaryKey"`
	Vector    pgvector.Vector `gorm:"type:vector(1536);not null"`
	Model     string          `gorm:"size:128;not null"`
	CreatedAt time.Time       `gorm:"not null"`
	UpdatedAt time.Time       `gorm:"not null"`
}

// BM25Row stores lexical statistics for a chunk to enable keyword-based scoring.
type BM25Row struct {
	ChunkID    int64          `gorm:"primaryKey"`
	Tokens     datatypes.JSON `gorm:"type:jsonb;not null"`
	TokenCount int            `gorm:"not null"`
	Tokenizer  string         `gorm:"size:64;not null"`
	CreatedAt  time.Time      `gorm:"not null"`
	UpdatedAt  time.Time      `gorm:"not null"`
}

// TableName overrides ensure stable table names for migrations.
func (Task) TableName() string { return "mcp_rag_tasks" }

func (Chunk) TableName() string { return "mcp_rag_chunks" }

func (Embedding) TableName() string { return "mcp_rag_embeddings" }

func (BM25Row) TableName() string { return "mcp_rag_bm25" }
