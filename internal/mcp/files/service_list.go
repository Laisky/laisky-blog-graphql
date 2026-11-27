package files

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// List returns directory listings for the given path.
func (s *Service) List(ctx context.Context, auth AuthContext, project, path string, depth, limit int) (ListResult, error) {
	if err := s.validateAuth(auth); err != nil {
		return ListResult{}, errors.WithStack(err)
	}
	if err := ValidateProject(project); err != nil {
		return ListResult{}, errors.WithStack(err)
	}
	if err := ValidatePath(path); err != nil {
		return ListResult{}, errors.WithStack(err)
	}
	if depth < 0 {
		return ListResult{}, errors.WithStack(NewError(ErrCodeInvalidOffset, "depth must be >= 0", false))
	}

	if limit <= 0 {
		limit = s.settings.ListLimitDefault
	}
	if limit > s.settings.ListLimitMax {
		limit = s.settings.ListLimitMax
	}

	overlay := overlayApplies(ctx, project)
	if depth == 0 {
		var entry FileEntry
		var exists bool
		err := s.withReadSnapshot(ctx, overlay, func(q sqlQueryer) error {
			var err error
			entry, exists, err = s.listSelfEntry(ctx, q, auth.APIKeyHash, project, path, overlay)
			return err
		})
		if err != nil {
			return ListResult{}, errors.WithStack(err)
		}
		if !exists {
			return ListResult{}, errors.WithStack(NewError(ErrCodeNotFound, "path not found", false))
		}
		return ListResult{Entries: []FileEntry{entry}, HasMore: false}, nil
	}

	entries, err := s.listDescendants(ctx, auth.APIKeyHash, project, path, depth, overlay)
	if err != nil {
		return ListResult{}, errors.WithStack(err)
	}

	if len(entries) == 0 {
		if path == "" {
			return ListResult{Entries: []FileEntry{}, HasMore: false}, nil
		}
		return ListResult{}, errors.WithStack(NewError(ErrCodeNotFound, "path not found", false))
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})

	hasMore := false
	if len(entries) > limit {
		hasMore = true
		entries = entries[:limit]
	}

	return ListResult{Entries: entries, HasMore: hasMore}, nil
}

// listSelfEntry returns the entry for the target path itself.
func (s *Service) listSelfEntry(ctx context.Context, q sqlQueryer, apiKeyHash, project, path string, overlay bool) (FileEntry, bool, error) {
	if path == "" {
		updatedAt, exists, err := s.findDirectoryUpdatedAt(ctx, q, apiKeyHash, project, path, overlay)
		if err != nil {
			return FileEntry{}, false, errors.WithStack(err)
		}
		if !exists {
			updatedAt = time.Time{}
		}

		return FileEntry{
			Name:      "",
			Path:      "",
			Type:      FileTypeDirectory,
			Size:      0,
			CreatedAt: time.Time{},
			UpdatedAt: updatedAt,
		}, true, nil
	}

	file, err := s.resolveActiveFile(ctx, q, apiKeyHash, project, path, overlay)
	if err == nil {
		return fileToEntry(*file), true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return FileEntry{}, false, errors.Wrap(err, "query file")
	}

	updatedAt, exists, err := s.findDirectoryUpdatedAt(ctx, q, apiKeyHash, project, path, overlay)
	if err != nil {
		return FileEntry{}, false, errors.WithStack(err)
	}
	if !exists {
		return FileEntry{}, false, nil
	}

	name := ""
	if path != "" {
		parts := strings.Split(path, "/")
		name = parts[len(parts)-1]
	}

	return FileEntry{
		Name:      name,
		Path:      path,
		Type:      FileTypeDirectory,
		Size:      0,
		CreatedAt: time.Time{},
		UpdatedAt: updatedAt,
	}, true, nil
}

// listDescendants builds file and directory entries from the visible files under
// path, which with overlay include the DefaultProject files the project does not
// hide. visibleDescendants reads them with one statement.
func (s *Service) listDescendants(ctx context.Context, apiKeyHash, project, path string, depth int, overlay bool) ([]FileEntry, error) {
	visible, err := s.visibleDescendants(ctx, s.db, apiKeyHash, project, path, overlay)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	entries := make(map[string]FileEntry)
	dirUpdated := make(map[string]time.Time)

	for _, file := range visible {
		size, createdAt, updatedAt := file.Size, file.CreatedAt, file.UpdatedAt
		rel := strings.TrimPrefix(file.Path, path)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			continue
		}
		segments := strings.Split(rel, "/")

		if len(segments) <= depth {
			entryPath := joinPath(path, rel)
			entries[entryPath] = FileEntry{
				Name:      segments[len(segments)-1],
				Path:      entryPath,
				Type:      FileTypeFile,
				Size:      size,
				CreatedAt: createdAt,
				UpdatedAt: updatedAt,
			}
		}

		for i := 1; i <= min(depth, len(segments)-1); i++ {
			dirRel := strings.Join(segments[:i], "/")
			dirPath := joinPath(path, dirRel)
			if updatedAt.After(dirUpdated[dirPath]) {
				dirUpdated[dirPath] = updatedAt
			}
			if _, ok := entries[dirPath]; !ok {
				entries[dirPath] = FileEntry{
					Name:      segments[i-1],
					Path:      dirPath,
					Type:      FileTypeDirectory,
					Size:      0,
					CreatedAt: time.Time{},
					UpdatedAt: time.Time{},
				}
			}
		}
	}

	result := make([]FileEntry, 0, len(entries))
	for path, entry := range entries {
		if entry.Type == FileTypeDirectory {
			if updatedAt, ok := dirUpdated[path]; ok {
				entry.UpdatedAt = updatedAt
			}
		}
		result = append(result, entry)
	}

	return result, nil
}

// joinPath concatenates base and relative segments.
func joinPath(base, rel string) string {
	if base == "" {
		return "/" + rel
	}
	return base + "/" + rel
}

// fileToEntry converts a File row to a FileEntry.
func fileToEntry(file File) FileEntry {
	name := file.Path
	if idx := strings.LastIndex(file.Path, "/"); idx >= 0 {
		name = file.Path[idx+1:]
	}
	return FileEntry{
		Name:      name,
		Path:      file.Path,
		Type:      FileTypeFile,
		Size:      file.Size,
		CreatedAt: file.CreatedAt,
		UpdatedAt: file.UpdatedAt,
	}
}
