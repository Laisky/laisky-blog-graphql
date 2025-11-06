package calllog

import (
	"time"

	gutils "github.com/Laisky/go-utils/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Record persists a single MCP tool invocation.
type Record struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey"`
	ToolName       string    `gorm:"type:varchar(64);not null;index"`
	APIKeyHash     string    `gorm:"type:char(64);index"`
	KeyPrefix      string    `gorm:"type:varchar(16);index"`
	Status         string    `gorm:"type:varchar(16);not null;index"`
	Cost           int       `gorm:"type:bigint;not null"`
	CostUnit       string    `gorm:"type:varchar(16);not null;default:'quota'"`
	DurationMillis int64     `gorm:"type:bigint"`
	Parameters     []byte    `gorm:"type:jsonb"`
	ErrorMessage   string    `gorm:"type:text"`
	OccurredAt     time.Time `gorm:"index"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// BeforeCreate ensures a UUID primary key is assigned before persistence.
func (r *Record) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = gutils.UUID7Bytes()
	}
	return nil
}
