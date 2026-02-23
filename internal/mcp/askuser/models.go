package askuser

import (
	"time"

	"github.com/google/uuid"
)

const (
	// StatusPending indicates the request is waiting for the user response.
	StatusPending = "pending"
	// StatusAnswered indicates the request has been answered by the user.
	StatusAnswered = "answered"
	// StatusCancelled indicates the request was cancelled by the caller before an answer was received.
	StatusCancelled = "cancelled"
	// StatusExpired indicates the request expired while waiting for an answer.
	StatusExpired = "expired"
)

// Request represents a single ask_user tool invocation persisted in the database.
type Request struct {
	ID           uuid.UUID
	Question     string
	Answer       *string
	Status       string
	APIKeyHash   string
	KeySuffix    string
	UserIdentity string
	AIIdentity   string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	AnsweredAt   *time.Time
}
