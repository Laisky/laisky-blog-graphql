package files

import (
	"context"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	"gorm.io/gorm"
)

// Write applies content updates to a file path.
func (s *Service) Write(ctx context.Context, auth AuthContext, project, path, content, encoding string, offset int64, mode WriteMode) (WriteResult, error) {
	if err := s.validateAuth(auth); err != nil {
		return WriteResult{}, errors.WithStack(err)
	}
	if err := ValidateProject(project); err != nil {
		return WriteResult{}, errors.WithStack(err)
	}
	if err := ValidatePath(path); err != nil {
		return WriteResult{}, errors.WithStack(err)
	}
	if path == "" {
		return WriteResult{}, errors.WithStack(NewError(ErrCodeInvalidPath, "path is required", false))
	}
	if offset < 0 {
		return WriteResult{}, errors.WithStack(NewError(ErrCodeInvalidOffset, "offset must be >= 0", false))
	}
	encoding, err := NormalizeContentEncoding(encoding)
	if err != nil {
		return WriteResult{}, errors.WithStack(err)
	}
	if mode == "" {
		mode = WriteModeAppend
	}
	if mode != WriteModeAppend && mode != WriteModeOverwrite && mode != WriteModeTruncate {
		return WriteResult{}, errors.WithStack(NewError(ErrCodeInvalidOffset, "invalid write mode", false))
	}
	if mode == WriteModeTruncate && offset != 0 {
		return WriteResult{}, errors.WithStack(NewError(ErrCodeInvalidOffset, "truncate requires offset 0", false))
	}

	payloadBytes := int64(len([]byte(content)))
	if err := ValidatePayloadSize(payloadBytes, s.settings.MaxPayloadBytes); err != nil {
		return WriteResult{}, errors.WithStack(err)
	}

	var bytesWritten int64
	err = s.lockProvider.WithProjectLock(ctx, s.db, auth.APIKeyHash, project, s.settings.LockTimeout, func(tx *gorm.DB) error {
		if err := s.ensureNoDescendantFile(ctx, tx, auth.APIKeyHash, project, path); err != nil {
			return err
		}
		if err := s.ensureNoParentFile(ctx, tx, auth.APIKeyHash, project, path); err != nil {
			return err
		}

		existing, findErr := s.findActiveFile(ctx, auth.APIKeyHash, project, path)
		if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return errors.Wrap(findErr, "query existing file")
		}

		now := s.clock()
		var newContent []byte
		var createdAt time.Time
		switch {
		case errors.Is(findErr, gorm.ErrRecordNotFound):
			createdAt = now
			newContent, err = applyWriteMode(nil, content, offset, mode)
		default:
			createdAt = existing.CreatedAt
			newContent, err = applyWriteMode(existing.Content, content, offset, mode)
		}
		if err != nil {
			return err
		}

		newSize := int64(len(newContent))
		if err := ValidateFileSize(newSize, s.settings.MaxFileBytes); err != nil {
			return err
		}
		if err := s.ensureProjectQuota(ctx, tx, auth.APIKeyHash, project, newSize, existing); err != nil {
			return err
		}

		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			record := &File{
				APIKeyHash: auth.APIKeyHash,
				Project:    project,
				Path:       path,
				Content:    newContent,
				Size:       newSize,
				CreatedAt:  createdAt,
				UpdatedAt:  now,
				Deleted:    false,
				DeletedAt:  nil,
			}
			if err := tx.WithContext(ctx).Create(record).Error; err != nil {
				return errors.Wrap(err, "create file")
			}
		} else {
			if err := tx.WithContext(ctx).Model(&File{}).
				Where("id = ?", existing.ID).
				Updates(map[string]any{
					"content":    newContent,
					"size":       newSize,
					"updated_at": now,
					"deleted":    false,
					"deleted_at": nil,
				}).Error; err != nil {
				return errors.Wrap(err, "update file")
			}
		}

		job := FileIndexJob{
			APIKeyHash:    auth.APIKeyHash,
			Project:       project,
			FilePath:      path,
			Operation:     "UPSERT",
			FileUpdatedAt: &now,
			Status:        "pending",
			RetryCount:    0,
			AvailableAt:   now,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if err := tx.WithContext(ctx).Create(&job).Error; err != nil {
			return errors.Wrap(err, "enqueue index job")
		}

		if err := s.storeCredentialEnvelope(ctx, auth, project, path, now); err != nil {
			return err
		}

		bytesWritten = payloadBytes
		return nil
	})
	if err != nil {
		return WriteResult{}, errors.WithStack(err)
	}

	_ = encoding
	return WriteResult{BytesWritten: bytesWritten}, nil
}

// Delete removes a file or directory tree.
func (s *Service) Delete(ctx context.Context, auth AuthContext, project, path string, recursive bool) (DeleteResult, error) {
	if err := s.validateAuth(auth); err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}
	if err := ValidateProject(project); err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}
	if err := ValidatePath(path); err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}
	if path == "" {
		return DeleteResult{}, errors.WithStack(NewError(ErrCodePermissionDenied, "root directory cannot be deleted", false))
	}

	var deletedCount int
	err := s.lockProvider.WithProjectLock(ctx, s.db, auth.APIKeyHash, project, s.settings.LockTimeout, func(tx *gorm.DB) error {
		now := s.clock()
		paths, err := s.resolveDeleteTargets(ctx, tx, auth.APIKeyHash, project, path, recursive)
		if err != nil {
			return err
		}
		if len(paths) == 0 {
			return errors.WithStack(NewError(ErrCodeNotFound, "path not found", false))
		}

		if err := tx.WithContext(ctx).Model(&File{}).
			Where("apikey_hash = ? AND project = ? AND path IN ? AND deleted = FALSE", auth.APIKeyHash, project, paths).
			Updates(map[string]any{
				"deleted":    true,
				"deleted_at": now,
				"updated_at": now,
			}).Error; err != nil {
			return errors.Wrap(err, "soft delete files")
		}

		for _, p := range paths {
			job := FileIndexJob{
				APIKeyHash:    auth.APIKeyHash,
				Project:       project,
				FilePath:      p,
				Operation:     "DELETE",
				FileUpdatedAt: &now,
				Status:        "pending",
				RetryCount:    0,
				AvailableAt:   now,
				CreatedAt:     now,
				UpdatedAt:     now,
			}
			if err := tx.WithContext(ctx).Create(&job).Error; err != nil {
				return errors.Wrap(err, "enqueue delete job")
			}
		}

		deletedCount = len(paths)
		return nil
	})
	if err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}
	return DeleteResult{DeletedCount: deletedCount}, nil
}

// applyWriteMode merges incoming content with existing data and returns new bytes.
func applyWriteMode(existing []byte, content string, offset int64, mode WriteMode) ([]byte, error) {
	incoming := []byte(content)
	switch mode {
	case WriteModeAppend:
		return append(existing, incoming...), nil
	case WriteModeTruncate:
		return append([]byte{}, incoming...), nil
	case WriteModeOverwrite:
		size := int64(len(existing))
		if offset > size {
			return nil, NewError(ErrCodeInvalidOffset, "offset beyond eof", false)
		}
		newLen := int64(len(incoming)) + offset
		if newLen < size {
			newLen = size
		}
		buf := make([]byte, newLen)
		copy(buf, existing)
		copy(buf[offset:], incoming)
		return buf, nil
	default:
		return nil, NewError(ErrCodeInvalidOffset, "unsupported write mode", false)
	}
}

// ensureNoDescendantFile validates that the target path has no child files.
func (s *Service) ensureNoDescendantFile(ctx context.Context, tx *gorm.DB, apiKeyHash, project, path string) error {
	prefix := buildPathPrefix(path)
	var count int64
	if err := tx.WithContext(ctx).Model(&File{}).
		Where("apikey_hash = ? AND project = ? AND path LIKE ? AND deleted = FALSE", apiKeyHash, project, prefix).
		Count(&count).Error; err != nil {
		return errors.Wrap(err, "check descendant files")
	}
	if count > 0 {
		return NewError(ErrCodeIsDirectory, "path has descendant files", false)
	}
	return nil
}

// ensureNoParentFile validates that no parent segment is an existing file.
func (s *Service) ensureNoParentFile(ctx context.Context, tx *gorm.DB, apiKeyHash, project, path string) error {
	parents := parentPaths(path)
	if len(parents) == 0 {
		return nil
	}
	var count int64
	if err := tx.WithContext(ctx).Model(&File{}).
		Where("apikey_hash = ? AND project = ? AND path IN ? AND deleted = FALSE", apiKeyHash, project, parents).
		Count(&count).Error; err != nil {
		return errors.Wrap(err, "check parent files")
	}
	if count > 0 {
		return NewError(ErrCodeNotDirectory, "parent path is a file", false)
	}
	return nil
}

// parentPaths returns all parent segments for a path.
func parentPaths(path string) []string {
	trimmed := strings.TrimPrefix(path, "/")
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) <= 1 {
		return nil
	}
	parents := make([]string, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		parents = append(parents, "/"+strings.Join(parts[:i], "/"))
	}
	return parents
}

// ensureProjectQuota enforces project storage limits.
func (s *Service) ensureProjectQuota(ctx context.Context, tx *gorm.DB, apiKeyHash, project string, newSize int64, existing *File) error {
	var total int64
	if err := tx.WithContext(ctx).Model(&File{}).
		Select("COALESCE(SUM(size), 0)").
		Where("apikey_hash = ? AND project = ? AND deleted = FALSE", apiKeyHash, project).
		Row().Scan(&total); err != nil {
		return errors.Wrap(err, "sum project size")
	}
	if existing != nil {
		total -= existing.Size
	}
	if total+newSize > s.settings.MaxProjectBytes {
		return NewError(ErrCodeQuotaExceeded, "project storage quota exceeded", false)
	}
	return nil
}

// resolveDeleteTargets determines which file paths should be deleted.
func (s *Service) resolveDeleteTargets(ctx context.Context, tx *gorm.DB, apiKeyHash, project, path string, recursive bool) ([]string, error) {
	if path == "" {
		return s.listAllFilePaths(ctx, tx, apiKeyHash, project)
	}

	var file File
	err := tx.WithContext(ctx).
		Where("apikey_hash = ? AND project = ? AND path = ? AND deleted = FALSE", apiKeyHash, project, path).
		First(&file).Error
	if err == nil {
		return []string{file.Path}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.Wrap(err, "query delete target")
	}

	paths, err := s.listDescendantPaths(ctx, tx, apiKeyHash, project, path)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}
	if !recursive {
		return nil, NewError(ErrCodeNotEmpty, "directory not empty", false)
	}
	return paths, nil
}

// listDescendantPaths returns all active descendant file paths for a directory.
func (s *Service) listDescendantPaths(ctx context.Context, tx *gorm.DB, apiKeyHash, project, path string) ([]string, error) {
	prefix := buildPathPrefix(path)
	rows, err := tx.WithContext(ctx).Model(&File{}).
		Select("path").
		Where("apikey_hash = ? AND project = ? AND path LIKE ? AND deleted = FALSE", apiKeyHash, project, prefix).
		Rows()
	if err != nil {
		return nil, errors.Wrap(err, "query descendant paths")
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if scanErr := rows.Scan(&p); scanErr != nil {
			return nil, errors.Wrap(scanErr, "scan descendant path")
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// listAllFilePaths returns all active file paths in a project.
func (s *Service) listAllFilePaths(ctx context.Context, tx *gorm.DB, apiKeyHash, project string) ([]string, error) {
	rows, err := tx.WithContext(ctx).Model(&File{}).
		Select("path").
		Where("apikey_hash = ? AND project = ? AND deleted = FALSE", apiKeyHash, project).
		Rows()
	if err != nil {
		return nil, errors.Wrap(err, "query project paths")
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if scanErr := rows.Scan(&p); scanErr != nil {
			return nil, errors.Wrap(scanErr, "scan project path")
		}
		paths = append(paths, p)
	}
	return paths, nil
}
