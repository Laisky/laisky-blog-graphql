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
	// ContentHash is the SHA-256 of the current stored bytes. It identifies the
	// content generation used to bind chunks and the file summary together
	// (docs/proposals/file_search_file_summaries.md §4.2).
	ContentHash string
	// SummaryContentHash is the content generation the persisted summary describes.
	SummaryContentHash string
	// SummaryStatus is the lifecycle state of the persisted summary.
	SummaryStatus string
}

// TableName returns the database table name.
func (File) TableName() string {
	return "mcp_files"
}
