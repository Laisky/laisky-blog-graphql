package files

import (
	"context"
	"time"

	errors "github.com/Laisky/errors/v2"
	"gorm.io/gorm"
)

// Stat returns metadata for the target path.
func (s *Service) Stat(ctx context.Context, auth AuthContext, project, path string) (StatResult, error) {
	if err := s.validateAuth(auth); err != nil {
		return StatResult{}, errors.WithStack(err)
	}
	if err := ValidateProject(project); err != nil {
		return StatResult{}, errors.WithStack(err)
	}
	if err := ValidatePath(path); err != nil {
		return StatResult{}, errors.WithStack(err)
	}
	if path == "" {
		updatedAt, exists, err := s.findDirectoryUpdatedAt(ctx, auth.APIKeyHash, project, path)
		if err != nil {
			return StatResult{}, errors.WithStack(err)
		}
		if !exists {
			updatedAt = time.Time{}
		}
		return StatResult{
			Exists:    true,
			Type:      FileTypeDirectory,
			Size:      0,
			CreatedAt: time.Time{},
			UpdatedAt: updatedAt,
		}, nil
	}

	file, err := s.findActiveFile(ctx, auth.APIKeyHash, project, path)
	if err == nil {
		return StatResult{
			Exists:    true,
			Type:      FileTypeFile,
			Size:      file.Size,
			CreatedAt: file.CreatedAt,
			UpdatedAt: file.UpdatedAt,
		}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return StatResult{}, errors.Wrap(err, "query file")
	}

	updatedAt, exists, err := s.findDirectoryUpdatedAt(ctx, auth.APIKeyHash, project, path)
	if err != nil {
		return StatResult{}, errors.WithStack(err)
	}
	if !exists {
		return StatResult{Exists: false}, nil
	}

	return StatResult{
		Exists:    true,
		Type:      FileTypeDirectory,
		Size:      0,
		CreatedAt: time.Time{},
		UpdatedAt: updatedAt,
	}, nil
}

// Read returns file content for a given path and byte range.
func (s *Service) Read(ctx context.Context, auth AuthContext, project, path string, offset, length int64) (ReadResult, error) {
	if err := s.validateAuth(auth); err != nil {
		return ReadResult{}, errors.WithStack(err)
	}
	if err := ValidateProject(project); err != nil {
		return ReadResult{}, errors.WithStack(err)
	}
	if err := ValidatePath(path); err != nil {
		return ReadResult{}, errors.WithStack(err)
	}
	if path == "" {
		return ReadResult{}, errors.WithStack(NewError(ErrCodeInvalidPath, "path is required", false))
	}
	if offset < 0 {
		return ReadResult{}, errors.WithStack(NewError(ErrCodeInvalidOffset, "offset must be >= 0", false))
	}
	if length < -1 {
		return ReadResult{}, errors.WithStack(NewError(ErrCodeInvalidOffset, "length must be -1 or >= 0", false))
	}

	file, err := s.findActiveFile(ctx, auth.APIKeyHash, project, path)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if exists, dirErr := s.directoryExists(ctx, auth.APIKeyHash, project, path); dirErr == nil && exists {
				return ReadResult{}, errors.WithStack(NewError(ErrCodeIsDirectory, "path is a directory", false))
			}
			return ReadResult{}, errors.WithStack(NewError(ErrCodeNotFound, "file not found", false))
		}
		return ReadResult{}, errors.Wrap(err, "query file")
	}

	data := file.Content
	if offset >= int64(len(data)) {
		return ReadResult{Content: "", ContentEncoding: "utf-8"}, nil
	}
	end := int64(len(data))
	if length >= 0 && offset+length < end {
		end = offset + length
	}
	payload := string(data[offset:end])

	return ReadResult{Content: payload, ContentEncoding: "utf-8"}, nil
}

// findActiveFile loads a non-deleted file row by path.
func (s *Service) findActiveFile(ctx context.Context, apiKeyHash, project, path string) (*File, error) {
	var file File
	err := s.db.WithContext(ctx).
		Where("apikey_hash = ? AND project = ? AND path = ? AND deleted = FALSE", apiKeyHash, project, path).
		First(&file).Error
	if err != nil {
		return nil, err
	}
	return &file, nil
}

// directoryExists reports whether the path has any active descendants.
func (s *Service) directoryExists(ctx context.Context, apiKeyHash, project, path string) (bool, error) {
	_, exists, err := s.findDirectoryUpdatedAt(ctx, apiKeyHash, project, path)
	return exists, err
}

// findDirectoryUpdatedAt returns the latest update time for descendants, if any.
func (s *Service) findDirectoryUpdatedAt(ctx context.Context, apiKeyHash, project, path string) (time.Time, bool, error) {
	prefix := buildPathPrefix(path)
	var rawValue any
	err := s.db.WithContext(ctx).
		Model(&File{}).
		Select("MAX(updated_at)").
		Where("apikey_hash = ? AND project = ? AND path LIKE ? AND deleted = FALSE", apiKeyHash, project, prefix).
		Row().Scan(&rawValue)
	if err != nil {
		return time.Time{}, false, errors.Wrap(err, "query directory updated_at")
	}
	if rawValue == nil {
		return time.Time{}, false, nil
	}
	updatedAt, err := parseDBTime(rawValue)
	if err != nil {
		return time.Time{}, false, errors.Wrap(err, "parse directory updated_at")
	}
	if updatedAt.IsZero() {
		return time.Time{}, false, nil
	}

	return updatedAt, true, nil
}

// parseDBTime converts database aggregate timestamp values to UTC time.
func parseDBTime(value any) (time.Time, error) {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC(), nil
	case string:
		return parseTimeString(typed)
	case []byte:
		return parseTimeString(string(typed))
	default:
		return time.Time{}, errors.Errorf("unsupported timestamp type %T", value)
	}
}

// parseTimeString parses common SQL timestamp formats and normalizes to UTC.
func parseTimeString(raw string) (time.Time, error) {
	formats := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	for _, format := range formats {
		parsed, err := time.Parse(format, raw)
		if err == nil {
			return parsed.UTC(), nil
		}
	}

	return time.Time{}, errors.Errorf("unsupported timestamp format: %s", raw)
}
