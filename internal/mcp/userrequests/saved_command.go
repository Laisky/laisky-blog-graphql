package userrequests

import (
	"time"

	"github.com/google/uuid"
)

const (
	// MaxSavedCommandsPerUser is the maximum number of saved commands a single user can store.
	MaxSavedCommandsPerUser = 100
	// MaxSavedCommandLabelLength is the maximum length allowed for a saved command's label.
	MaxSavedCommandLabelLength = 255
	// MaxSavedCommandContentLength is the maximum length allowed for a saved command's content.
	MaxSavedCommandContentLength = 10000
)

// SavedCommand represents a reusable command template stored by a user for quick access.
type SavedCommand struct {
	ID           uuid.UUID
	Label        string
	Content      string
	SortOrder    int
	APIKeyHash   string
	KeySuffix    string
	UserIdentity string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TableName specifies the database table name for saved commands.
func (SavedCommand) TableName() string {
	return "mcp_saved_commands"
}

// SavedCommandDTO is the data transfer object for saved command API responses.
type SavedCommandDTO struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Content   string `json:"content"`
	SortOrder int    `json:"sort_order"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ToDTO converts a SavedCommand model to its DTO representation.
func (c *SavedCommand) ToDTO() SavedCommandDTO {
	return SavedCommandDTO{
		ID:        c.ID.String(),
		Label:     c.Label,
		Content:   c.Content,
		SortOrder: c.SortOrder,
		CreatedAt: c.CreatedAt.Format(time.RFC3339),
		UpdatedAt: c.UpdatedAt.Format(time.RFC3339),
	}
}
