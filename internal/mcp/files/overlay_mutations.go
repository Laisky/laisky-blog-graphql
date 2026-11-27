package files

import (
	"context"
	"database/sql"

	errors "github.com/Laisky/errors/v2"
)

// deleteFiles deletes the files path resolves to, each in the physical project
// that stores it, under the locks runUserMutation selects. With overlay,
// deleting a path served by DefaultProject removes that default for every
// project, while deleting a project file that shadowed a default reveals the
// default again.
func (s *Service) deleteFiles(ctx context.Context, auth AuthContext, project, path string, recursive bool) (int, error) {
	conditions := filePreconditionsFromContext(ctx, auth, project, path, FileOperationDelete)
	enqueueIndex := systemOwnerFromContext(ctx) == ""
	deleted := 0
	err := s.runUserMutation(ctx, auth.APIKeyHash, project, []string{project}, []string{path}, func(tx *sql.Tx, overlay bool) error {
		if err := s.checkVersionTx(ctx, tx, auth, project, path, conditions.ExpectedVersion, false, overlay); err != nil {
			return err
		}
		targets, err := s.deleteTargetsTx(ctx, tx, auth.APIKeyHash, project, path, recursive, overlay)
		if err != nil {
			return err
		}
		now := s.clock()
		layers, grouped := groupPathsByProject(targets)
		for _, layer := range layers {
			if err := s.softDeletePathsTx(ctx, tx, auth, layer, grouped[layer], now, enqueueIndex); err != nil {
				return err
			}
		}
		deleted = len(targets)
		return nil
	})
	return deleted, err
}

// deleteTargetsTx resolves the files a delete removes: the file the project
// shows at path, or with recursive every file it shows below path.
func (s *Service) deleteTargetsTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, path string, recursive, overlay bool) ([]layerFile, error) {
	if path != "" {
		file, err := s.resolveActiveFile(ctx, tx, apiKeyHash, project, path, overlay)
		if err == nil {
			return []layerFile{{ID: file.ID, Project: file.Project, Path: file.Path}}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, errors.Wrap(err, "query delete target")
		}
	}
	visible, err := s.visibleDescendants(ctx, tx, apiKeyHash, project, path, overlay)
	if err != nil {
		return nil, err
	}
	if len(visible) == 0 {
		return nil, NewError(ErrCodeNotFound, "path not found", false)
	}
	if !recursive && path != "" {
		return nil, NewError(ErrCodeNotEmpty, "directory not empty", false)
	}
	return visible, nil
}
