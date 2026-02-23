package files

import "time"

// File represents a stored file row.
type File struct {
	ID         uint64
	APIKeyHash string
	Project    string
	Path       string
	Content    []byte
	Size       int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Deleted    bool
	DeletedAt  *time.Time
}

// TableName returns the database table name.
func (File) TableName() string {
	return "mcp_files"
}
