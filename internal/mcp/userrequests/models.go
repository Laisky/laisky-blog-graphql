package userrequests

import (
	"time"

	gutils "github.com/Laisky/go-utils/v6"
	"github.com/google/uuid"
	"gorm.io/gorm"
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
	ID           uuid.UUID `gorm:"type:uuid;primaryKey"`
	Content      string    `gorm:"type:text;not null"`
	Status       string    `gorm:"type:varchar(16);not null;index"`
	TaskID       string    `gorm:"type:varchar(255);not null;default:'default';index"`
	SortOrder    int       `gorm:"type:integer;not null;default:0"`
	APIKeyHash   string    `gorm:"type:char(64);not null;index"`
	KeySuffix    string    `gorm:"type:varchar(16);not null"`
	UserIdentity string    `gorm:"type:varchar(255);not null"`
	ConsumedAt   *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TableName forces the gorm table to match the required schema name.
func (Request) TableName() string {
	return "mcp_user_requests"
}

// BeforeCreate fills the ID with a UUIDv7-compatible value when missing.
func (r *Request) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = gutils.UUID7Bytes()
	}
	return nil
}
