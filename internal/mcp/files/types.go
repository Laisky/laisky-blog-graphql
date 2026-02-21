package files

import "time"

// FileType describes the logical type of a FileEntry.
type FileType string

const (
	// FileTypeFile represents a stored file.
	FileTypeFile FileType = "FILE"
	// FileTypeDirectory represents a synthesized directory path.
	FileTypeDirectory FileType = "DIRECTORY"
)

// WriteMode describes how file_write applies content to a target path.
type WriteMode string

const (
	// WriteModeAppend always writes at EOF.
	WriteModeAppend WriteMode = "APPEND"
	// WriteModeOverwrite writes content at the provided offset.
	WriteModeOverwrite WriteMode = "OVERWRITE"
	// WriteModeTruncate clears the file before writing.
	WriteModeTruncate WriteMode = "TRUNCATE"
)

// FileEntry summarizes a file or directory path for file_list responses.
type FileEntry struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Type      FileType  `json:"type"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ChunkEntry describes a file chunk returned by file_search.
type ChunkEntry struct {
	FilePath           string  `json:"file_path"`
	FileSeekStartBytes int64   `json:"file_seek_start_bytes"`
	FileSeekEndBytes   int64   `json:"file_seek_end_bytes"`
	ChunkContent       string  `json:"chunk_content"`
	Score              float64 `json:"score"`
}

// AuthContext carries trusted caller identity for file operations.
type AuthContext struct {
	APIKey       string
	APIKeyHash   string
	UserIdentity string
}

// StatResult returns the file_stat outcome.
type StatResult struct {
	Exists    bool
	Type      FileType
	Size      int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ReadResult returns the file_read payload.
type ReadResult struct {
	Content         string
	ContentEncoding string
}

// WriteResult returns the file_write outcome.
type WriteResult struct {
	BytesWritten int64
}

// DeleteResult returns the file_delete outcome.
type DeleteResult struct {
	DeletedCount int
}

// RenameResult returns the file_rename outcome.
type RenameResult struct {
	MovedCount int
}

// ListResult returns the file_list outcome.
type ListResult struct {
	Entries []FileEntry
	HasMore bool
}

// SearchResult returns the file_search outcome.
type SearchResult struct {
	Chunks []ChunkEntry
}
