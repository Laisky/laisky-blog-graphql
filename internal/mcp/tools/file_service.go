package tools

import (
	"context"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// FileService exposes file operations for MCP tools.
type FileService interface {
	Stat(context.Context, files.AuthContext, string, string) (files.StatResult, error)
	Read(context.Context, files.AuthContext, string, string, int64, int64) (files.ReadResult, error)
	Write(context.Context, files.AuthContext, string, string, string, string, int64, files.WriteMode) (files.WriteResult, error)
	Delete(context.Context, files.AuthContext, string, string, bool) (files.DeleteResult, error)
	List(context.Context, files.AuthContext, string, string, int, int) (files.ListResult, error)
	Search(context.Context, files.AuthContext, string, string, string, int) (files.SearchResult, error)
}
