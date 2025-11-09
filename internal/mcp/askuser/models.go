package askuser

import (
	"time"

	gutils "github.com/Laisky/go-utils/v6"
	"github.com/google/uuid"
	"gorm.io/gorm"
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
	ID           uuid.UUID `gorm:"type:uuid;primaryKey"`
	Question     string    `gorm:"type:text;not null"`
	Answer       *string   `gorm:"type:text"`
	Status       string    `gorm:"type:varchar(16);not null;index"`
	APIKeyHash   string    `gorm:"type:char(64);not null;index"`
	KeySuffix    string    `gorm:"type:varchar(16);not null"`
	UserIdentity string    `gorm:"type:varchar(255);not null"`
	AIIdentity   string    `gorm:"type:varchar(255);not null"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
	AnsweredAt   *time.Time
}

// BeforeCreate hook ensures the primary key is populated for new records.
func (r *Request) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = gutils.UUID7Bytes()
	}
	return nil
}
