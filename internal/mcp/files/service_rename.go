package files

import (
	"context"
	"strings"

	errors "github.com/Laisky/errors/v2"
	"gorm.io/gorm"
)

// renameSourceFile represents an active file selected as a rename source.
type renameSourceFile struct {
	ID   uint64
	Path string
}

// renameMapping represents one old path to new path update during rename.
type renameMapping struct {
	ID      uint64
	OldPath string
	NewPath string
}

// Rename renames or moves a file path or directory subtree.
func (s *Service) Rename(ctx context.Context, auth AuthContext, project, fromPath, toPath string, overwrite bool) (RenameResult, error) {
	if err := s.validateAuth(auth); err != nil {
		return RenameResult{}, errors.WithStack(err)
	}
	if err := ValidateProject(project); err != nil {
		return RenameResult{}, errors.WithStack(err)
	}
	if err := ValidatePath(fromPath); err != nil {
		return RenameResult{}, errors.WithStack(err)
	}
	if err := ValidatePath(toPath); err != nil {
		return RenameResult{}, errors.WithStack(err)
	}
	if fromPath == "" || toPath == "" {
		return RenameResult{}, errors.WithStack(NewError(ErrCodeInvalidPath, "source and destination paths must be non-root", false))
	}
	if fromPath == toPath {
		return RenameResult{MovedCount: 0}, nil
	}

	movedCount := 0
	err := s.lockProvider.WithProjectLock(ctx, s.db, auth.APIKeyHash, project, s.settings.LockTimeout, func(tx *gorm.DB) error {
		sourceFiles, sourceIsDirectory, err := s.resolveRenameSources(ctx, tx, auth.APIKeyHash, project, fromPath)
		if err != nil {
			return err
		}

		if sourceIsDirectory && strings.HasPrefix(toPath, fromPath+"/") {
			return NewError(ErrCodeInvalidPath, "destination cannot be within source subtree", false)
		}

		if err := s.ensureNoParentFile(ctx, tx, auth.APIKeyHash, project, toPath); err != nil {
			return err
		}

		mappings, err := buildRenameMappings(sourceFiles, fromPath, toPath, sourceIsDirectory)
		if err != nil {
			return err
		}

		now := s.clock()
		overwritePaths, err := s.validateRenameDestinations(ctx, tx, auth.APIKeyHash, project, mappings, toPath, sourceIsDirectory, overwrite)
		if err != nil {
			return err
		}

		if len(overwritePaths) > 0 {
			if err := tx.WithContext(ctx).Model(&File{}).
				Where("apikey_hash = ? AND project = ? AND path IN ? AND deleted = FALSE", auth.APIKeyHash, project, overwritePaths).
				Updates(map[string]any{"deleted": true, "deleted_at": now, "updated_at": now}).Error; err != nil {
				return errors.Wrap(err, "soft delete overwritten destination files")
			}
		}

		for _, mapping := range mappings {
			if err := tx.WithContext(ctx).Model(&File{}).
				Where("id = ?", mapping.ID).
				Updates(map[string]any{"path": mapping.NewPath, "updated_at": now}).Error; err != nil {
				return errors.Wrap(err, "apply rename path remap")
			}

			deleteJob := FileIndexJob{
				APIKeyHash:    auth.APIKeyHash,
				Project:       project,
				FilePath:      mapping.OldPath,
				Operation:     "DELETE",
				FileUpdatedAt: &now,
				Status:        "pending",
				RetryCount:    0,
				AvailableAt:   now,
				CreatedAt:     now,
				UpdatedAt:     now,
			}
			if err := tx.WithContext(ctx).Create(&deleteJob).Error; err != nil {
				return errors.Wrap(err, "enqueue rename delete job")
			}

			upsertJob := FileIndexJob{
				APIKeyHash:    auth.APIKeyHash,
				Project:       project,
				FilePath:      mapping.NewPath,
				Operation:     "UPSERT",
				FileUpdatedAt: &now,
				Status:        "pending",
				RetryCount:    0,
				AvailableAt:   now,
				CreatedAt:     now,
				UpdatedAt:     now,
			}
			if err := tx.WithContext(ctx).Create(&upsertJob).Error; err != nil {
				return errors.Wrap(err, "enqueue rename upsert job")
			}

			if err := s.storeCredentialEnvelope(ctx, auth, project, mapping.NewPath, now); err != nil {
				return err
			}
		}

		for _, overwrittenPath := range overwritePaths {
			job := FileIndexJob{
				APIKeyHash:    auth.APIKeyHash,
				Project:       project,
				FilePath:      overwrittenPath,
				Operation:     "DELETE",
				FileUpdatedAt: &now,
				Status:        "pending",
				RetryCount:    0,
				AvailableAt:   now,
				CreatedAt:     now,
				UpdatedAt:     now,
			}
			if err := tx.WithContext(ctx).Create(&job).Error; err != nil {
				return errors.Wrap(err, "enqueue overwrite delete job")
			}
		}

		movedCount = len(mappings)
		return nil
	})
	if err != nil {
		return RenameResult{}, errors.WithStack(err)
	}

	return RenameResult{MovedCount: movedCount}, nil
}

// resolveRenameSources resolves source files for a file or directory rename.
func (s *Service) resolveRenameSources(ctx context.Context, tx *gorm.DB, apiKeyHash, project, fromPath string) ([]renameSourceFile, bool, error) {
	var exact File
	err := tx.WithContext(ctx).
		Select("id", "path").
		Where("apikey_hash = ? AND project = ? AND path = ? AND deleted = FALSE", apiKeyHash, project, fromPath).
		First(&exact).Error
	if err == nil {
		return []renameSourceFile{{ID: exact.ID, Path: exact.Path}}, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, errors.Wrap(err, "query rename source file")
	}

	var descendants []renameSourceFile
	prefix := buildPathPrefix(fromPath)
	if err := tx.WithContext(ctx).
		Model(&File{}).
		Select("id", "path").
		Where("apikey_hash = ? AND project = ? AND path LIKE ? AND deleted = FALSE", apiKeyHash, project, prefix).
		Order("path ASC").
		Find(&descendants).Error; err != nil {
		return nil, false, errors.Wrap(err, "query rename source descendants")
	}
	if len(descendants) == 0 {
		return nil, false, NewError(ErrCodeNotFound, "source path not found", false)
	}

	return descendants, true, nil
}

// buildRenameMappings computes destination paths for the selected rename source files.
func buildRenameMappings(sourceFiles []renameSourceFile, fromPath, toPath string, sourceIsDirectory bool) ([]renameMapping, error) {
	mappings := make([]renameMapping, 0, len(sourceFiles))
	for _, source := range sourceFiles {
		newPath := toPath
		if sourceIsDirectory {
			suffix := strings.TrimPrefix(source.Path, fromPath)
			if !strings.HasPrefix(source.Path, fromPath+"/") || suffix == source.Path {
				return nil, NewError(ErrCodeInvalidPath, "invalid directory rename source mapping", false)
			}
			newPath = toPath + suffix
		}
		mappings = append(mappings, renameMapping{ID: source.ID, OldPath: source.Path, NewPath: newPath})
	}

	return mappings, nil
}

// validateRenameDestinations checks rename collisions and returns overwrite targets.
func (s *Service) validateRenameDestinations(
	ctx context.Context,
	tx *gorm.DB,
	apiKeyHash, project string,
	mappings []renameMapping,
	toPath string,
	sourceIsDirectory bool,
	overwrite bool,
) ([]string, error) {
	destinationPaths := make([]string, 0, len(mappings))
	sourcePathByID := make(map[uint64]string, len(mappings))
	pathSet := make(map[string]struct{}, len(mappings))
	for _, mapping := range mappings {
		sourcePathByID[mapping.ID] = mapping.OldPath
		if _, exists := pathSet[mapping.NewPath]; exists {
			return nil, NewError(ErrCodeAlreadyExists, "destination path collision", false)
		}
		pathSet[mapping.NewPath] = struct{}{}
		destinationPaths = append(destinationPaths, mapping.NewPath)
	}

	if !sourceIsDirectory {
		var descendantCount int64
		if err := tx.WithContext(ctx).Model(&File{}).
			Where("apikey_hash = ? AND project = ? AND path LIKE ? AND deleted = FALSE", apiKeyHash, project, buildPathPrefix(toPath)).
			Count(&descendantCount).Error; err != nil {
			return nil, errors.Wrap(err, "check destination descendants")
		}
		if descendantCount > 0 {
			return nil, NewError(ErrCodeAlreadyExists, "destination path already exists", false)
		}
	}

	if sourceIsDirectory {
		var destinationRootFileCount int64
		if err := tx.WithContext(ctx).Model(&File{}).
			Where("apikey_hash = ? AND project = ? AND path = ? AND deleted = FALSE", apiKeyHash, project, toPath).
			Count(&destinationRootFileCount).Error; err != nil {
			return nil, errors.Wrap(err, "check destination root collision")
		}
		if destinationRootFileCount > 0 {
			return nil, NewError(ErrCodeAlreadyExists, "destination path already exists", false)
		}
	}

	var destinationFiles []File
	if err := tx.WithContext(ctx).
		Model(&File{}).
		Select("id", "path").
		Where("apikey_hash = ? AND project = ? AND path IN ? AND deleted = FALSE", apiKeyHash, project, destinationPaths).
		Find(&destinationFiles).Error; err != nil {
		return nil, errors.Wrap(err, "query destination collisions")
	}

	overwritePaths := make([]string, 0, len(destinationFiles))
	for _, destination := range destinationFiles {
		if sourcePath, ok := sourcePathByID[destination.ID]; ok {
			if sourcePath == destination.Path {
				continue
			}
		}

		if !overwrite || sourceIsDirectory {
			return nil, NewError(ErrCodeAlreadyExists, "destination path already exists", false)
		}

		overwritePaths = append(overwritePaths, destination.Path)
	}

	return overwritePaths, nil
}
