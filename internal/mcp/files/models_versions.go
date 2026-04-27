package files

import "time"

// FileVersion represents a snapshot of a file's prior content.
type FileVersion struct {
	ID           uint64
	APIKeyHash   string
	Project      string
	Path         string
	Content      []byte
	Size         int64
	CreatedAt    time.Time
	SourceFileID *uint64
}

func (FileVersion) TableName() string { return "mcp_file_versions" }
