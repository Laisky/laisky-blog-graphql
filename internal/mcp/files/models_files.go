package files

import "time"

// File represents a stored file row.
type File struct {
	ID         uint64     `gorm:"primaryKey"`
	APIKeyHash string     `gorm:"column:apikey_hash;size:64;not null"`
	Project    string     `gorm:"size:128;not null"`
	Path       string     `gorm:"size:1024;not null"`
	Content    []byte     `gorm:"type:bytea;not null"`
	Size       int64      `gorm:"not null"`
	CreatedAt  time.Time  `gorm:"not null"`
	UpdatedAt  time.Time  `gorm:"not null"`
	Deleted    bool       `gorm:"not null"`
	DeletedAt  *time.Time `gorm:"column:deleted_at"`
}

// TableName returns the database table name.
func (File) TableName() string {
	return "mcp_files"
}
