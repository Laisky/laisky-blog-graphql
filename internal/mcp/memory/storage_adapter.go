package memory

import (
	"context"
	"strings"

	errors "github.com/Laisky/errors/v2"
	filesdk "github.com/Laisky/go-utils/v6/agents/files"
	memorystorage "github.com/Laisky/go-utils/v6/agents/memory/storage"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// storageAdapter adapts internal FileIO service to memory storage engine API.
type storageAdapter struct {
	fileService *files.Service
	auth        files.AuthContext
}

// newStorageAdapter constructs a memory storage adapter for one authenticated request context.
func newStorageAdapter(fileService *files.Service, auth files.AuthContext) (*storageAdapter, error) {
	if fileService == nil {
		return nil, errors.WithStack(NewError(ErrCodeInternal, "file service is required", false))
	}
	if strings.TrimSpace(auth.APIKeyHash) == "" {
		return nil, errors.WithStack(NewError(ErrCodePermissionDenied, "missing authorization", false))
	}

	return &storageAdapter{
		fileService: fileService,
		auth:        auth,
	}, nil
}

// Read reads file content bytes from storage.
func (adapter *storageAdapter) Read(ctx context.Context, project, path string, offset, length int64) (string, error) {
	result, err := adapter.fileService.Read(ctx, adapter.auth, project, path, offset, length)
	if err != nil {
		return "", errors.WithStack(err)
	}
	return result.Content, nil
}

// Write writes file content using the selected write mode.
func (adapter *storageAdapter) Write(ctx context.Context, project, path, content string, mode memorystorage.WriteMode, offset int64) error {
	writeMode, err := toFileWriteMode(mode)
	if err != nil {
		return errors.WithStack(err)
	}
	_, err = adapter.fileService.Write(ctx, adapter.auth, project, path, content, "utf-8", offset, writeMode)
	if err != nil {
		return errors.WithStack(err)
	}
	return nil
}

// Stat returns metadata for one storage path.
func (adapter *storageAdapter) Stat(ctx context.Context, project, path string) (memorystorage.FileInfo, error) {
	result, err := adapter.fileService.Stat(ctx, adapter.auth, project, path)
	if err != nil {
		return memorystorage.FileInfo{}, errors.WithStack(err)
	}

	fileType := filesdk.FileTypeUnknown
	if result.Type == files.FileTypeFile {
		fileType = filesdk.FileTypeFile
	} else if result.Type == files.FileTypeDirectory {
		fileType = filesdk.FileTypeDirectory
	}

	return memorystorage.FileInfo{
		Path:      path,
		Exists:    result.Exists,
		Type:      fileType,
		SizeBytes: result.Size,
		UpdatedAt: result.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	}, nil
}

// List lists descendants under one path.
func (adapter *storageAdapter) List(ctx context.Context, project, path string, depth, limit int) (entries []memorystorage.FileInfo, hasMore bool, err error) {
	result, err := adapter.fileService.List(ctx, adapter.auth, project, path, depth, limit)
	if err != nil {
		if typed, ok := files.AsError(err); ok {
			if typed.Code == files.ErrCodeNotFound {
				return []memorystorage.FileInfo{}, false, nil
			}
		}
		return nil, false, errors.WithStack(err)
	}

	output := make([]memorystorage.FileInfo, 0, len(result.Entries))
	for _, entry := range result.Entries {
		fileType := filesdk.FileTypeUnknown
		if entry.Type == files.FileTypeFile {
			fileType = filesdk.FileTypeFile
		} else if entry.Type == files.FileTypeDirectory {
			fileType = filesdk.FileTypeDirectory
		}

		output = append(output, memorystorage.FileInfo{
			Path:      entry.Path,
			Exists:    true,
			Type:      fileType,
			SizeBytes: entry.Size,
			UpdatedAt: entry.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		})
	}

	return output, result.HasMore, nil
}

// Search performs hybrid retrieval under the project path prefix.
func (adapter *storageAdapter) Search(ctx context.Context, project, query, pathPrefix string, limit int) ([]memorystorage.FileChunk, error) {
	result, err := adapter.fileService.Search(ctx, adapter.auth, project, query, pathPrefix, limit)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	chunks := make([]memorystorage.FileChunk, 0, len(result.Chunks))
	for _, chunk := range result.Chunks {
		chunks = append(chunks, memorystorage.FileChunk{
			FilePath:   chunk.FilePath,
			StartBytes: chunk.FileSeekStartBytes,
			EndBytes:   chunk.FileSeekEndBytes,
			Content:    chunk.ChunkContent,
			Score:      chunk.Score,
		})
	}

	return chunks, nil
}

// Delete removes a file path or subtree.
func (adapter *storageAdapter) Delete(ctx context.Context, project, path string, recursive bool) error {
	_, err := adapter.fileService.Delete(ctx, adapter.auth, project, path, recursive)
	if err != nil {
		return errors.WithStack(err)
	}
	return nil
}

// toFileWriteMode converts memory write mode to internal FileIO write mode.
func toFileWriteMode(mode memorystorage.WriteMode) (files.WriteMode, error) {
	switch mode {
	case memorystorage.WriteModeAppend:
		return files.WriteModeAppend, nil
	case memorystorage.WriteModeOverwrite:
		return files.WriteModeOverwrite, nil
	case memorystorage.WriteModeTruncate:
		return files.WriteModeTruncate, nil
	default:
		return "", errors.WithStack(NewError(ErrCodeInvalidArgument, "invalid write mode", false))
	}
}
