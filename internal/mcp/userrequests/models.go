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

	// Images hold attachments associated with this request, sorted by display order.
	// The slice is nil / empty for text-only requests.
	Images []RequestImage
}

// TableName returns the storage table name for requests.
func (Request) TableName() string {
	return "mcp_user_requests"
}

// RequestImage is the metadata-only representation of an image attachment.
// The actual PNG bytes live in MinIO keyed by StorageKey.
type RequestImage struct {
	ID           uuid.UUID
	RequestID    uuid.UUID
	UserIdentity string
	APIKeyHash   string
	StorageKey   string
	SHA256       string
	SizeBytes    int64
	Width        int
	Height       int
	MIMEType     string
	OriginalMIME string
	SourceURL    string
	SortOrder    int
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

// UploadedImage represents the result of the server-side normalization pipeline,
// ready to be PUT into the object store and recorded in the database.
type UploadedImage struct {
	SHA256       string
	StorageKey   string
	PNG          []byte
	SizeBytes    int64
	Width        int
	Height       int
	OriginalMIME string
	SourceURL    string
}
