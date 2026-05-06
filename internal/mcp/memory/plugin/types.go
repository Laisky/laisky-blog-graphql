// Phase 1 re-exports — moving the canonical definitions is deferred to a later phase to keep the diff additive.
package plugin

import "github.com/Laisky/laisky-blog-graphql/internal/mcp/files"

type (
	AuthContext  = files.AuthContext
	StatResult   = files.StatResult
	ReadResult   = files.ReadResult
	WriteResult  = files.WriteResult
	DeleteResult = files.DeleteResult
	RenameResult = files.RenameResult
	ListResult   = files.ListResult
	SearchResult = files.SearchResult
	FileEntry    = files.FileEntry
	ChunkEntry   = files.ChunkEntry
	WriteMode    = files.WriteMode
	FileType     = files.FileType
)

const (
	WriteModeAppend    = files.WriteModeAppend
	WriteModeOverwrite = files.WriteModeOverwrite
	WriteModeTruncate  = files.WriteModeTruncate

	FileTypeFile      = files.FileTypeFile
	FileTypeDirectory = files.FileTypeDirectory
)
