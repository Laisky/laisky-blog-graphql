package userrequests

import (
	"time"

	"github.com/google/uuid"
)

const (
	// StatusPending marks a user request that has not yet been delivered to an AI agent.
	StatusPending = "pending"
	// StatusConsumed marks a request that has already been handed to an AI agent.
	StatusConsumed = "consumed"
	// DefaultTaskID is used when the caller does not specify a task identifier.
	DefaultTaskID = "default"
)

// Request represents a single piece of user-provided feedback destined for an AI agent.
type Request struct {
	ID           uuid.UUID
	Content      string
	Status       string
	TaskID       string
	SortOrder    int
	APIKeyHash   string
	KeySuffix    string
	UserIdentity string
	ConsumedAt   *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TableName returns the storage table name for requests.
func (Request) TableName() string {
	return "mcp_user_requests"
}
