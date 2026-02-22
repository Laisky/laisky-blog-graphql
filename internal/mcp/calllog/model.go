package calllog

import (
	"time"

	"github.com/google/uuid"
)

// Record persists a single MCP tool invocation.
type Record struct {
	ID             uuid.UUID
	ToolName       string
	APIKeyHash     string
	KeyPrefix      string
	Status         string
	Cost           int
	CostUnit       string
	DurationMillis int64
	Parameters     []byte
	ErrorMessage   string
	OccurredAt     time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
