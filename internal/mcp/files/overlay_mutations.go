package files

import (
	"context"
	"database/sql"
	"strings"

	errors "github.com/Laisky/errors/v2"
)

// overlayRenameMove is one file moved within its own physical project.
type overlayRenameMove struct {
	renameMapping
	Project string
}

// deleteOverlayTx deletes the files a path resolves to in the merged view. Each
// file is deleted in the physical project that stores it, so deleting a path
// served by DefaultProject removes that default for every project, while
// deleting a project file that shadowed a default reveals the default again.
// The caller holds both project locks.
func (s *Service) deleteOverlayTx(ctx context.Context, tx *sql.Tx, auth AuthContext, project, path string, recursive bool,
	conditions FilePreconditions, state *systemStateRun,
) (int, error) {
	if err := s.checkEffectiveVersionTx(ctx, tx, auth.APIKeyHash, project, path, conditions.ExpectedVersion, false); err != nil {
		return 0, err
	}
	targets, err := s.overlayDeleteTargetsTx(ctx, tx, auth.APIKeyHash, project, path, recursive)
	if err != nil {
		return 0, err
	}
	now := s.clock()
	projects, grouped := groupPathsByProject(targets)
	for _, layer := range projects {
		// Index jobs follow the ordinary user Delete; transactional plugin
		// deletes keep their own catalog instead.
		if err := s.softDeletePathsTx(ctx, tx, auth, layer, grouped[layer], now, state == nil); err != nil {
			return 0, err
		}
		if err := state.deleted(ctx, s, tx, layer, grouped[layer]); err != nil {
			return 0, err
		}
	}
	return len(targets), nil
}

// overlayDeleteTargetsTx resolves the visible files a delete removes.
func (s *Service) overlayDeleteTargetsTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, path string, recursive bool) ([]layerFile, error) {
	file, err := s.resolveActiveFile(ctx, tx, apiKeyHash, project, path)
	if err == nil {
		return []layerFile{{ID: file.ID, Project: file.Project, Path: file.Path}}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, errors.Wrap(err, "query delete target")
	}
	visible, err := s.visibleDescendants(ctx, tx, apiKeyHash, project, path)
	if err != nil {
		return nil, err
	}
	if len(visible) == 0 {
		return nil, NewError(ErrCodeNotFound, "path not found", false)
	}
	if !recursive {
		return nil, NewError(ErrCodeNotEmpty, "directory not empty", false)
	}
	return visible, nil
}

// renameOverlayTx renames the files a path resolves to in the merged view. Every
// file moves within the physical project that stores it, so renaming a default
// renames it for every project. Collisions are judged on the merged view. The
// caller holds both project locks.
func (s *Service) renameOverlayTx(ctx context.Context, tx *sql.Tx, auth AuthContext, project, fromPath, toPath string, overwrite bool,
	conditions FilePreconditions, state *systemStateRun,
) (int, error) {
	owner := systemOwnerFromContext(ctx)
	if err := checkRenamePreconditions(fromPath, toPath, overwrite, conditions, func(path, expected string, createOnly bool) error {
		return s.checkEffectiveVersionTx(ctx, tx, auth.APIKeyHash, project, path, expected, createOnly)
	}); err != nil {
		return 0, err
	}
	if fromPath == toPath {
		return 0, nil
	}
	moves, overwritten, err := s.planOverlayRenameTx(ctx, tx, auth.APIKeyHash, project, fromPath, toPath, overwrite)
	if err != nil {
		return 0, err
	}
	now := s.clock()
	deleteProjects, deletes := groupPathsByProject(overwritten)
	for _, layer := range deleteProjects {
		// Replaced destinations keep a history snapshot, and their index delete
		// jobs precede the moved file's upsert for the same name.
		if err := s.softDeletePathsTx(ctx, tx, auth, layer, deletes[layer], now, owner == ""); err != nil {
			return 0, err
		}
		if err := state.deleted(ctx, s, tx, layer, deletes[layer]); err != nil {
			return 0, err
		}
	}
	for _, layer := range []string{project, DefaultProject} {
		var mappings []renameMapping
		for _, move := range moves {
			if move.Project == layer {
				mappings = append(mappings, move.renameMapping)
			}
		}
		if len(mappings) == 0 {
			continue
		}
		if err := s.applyRenameMappingsTx(ctx, tx, auth, layer, owner, mappings, now); err != nil {
			return 0, err
		}
		if err := state.moved(ctx, s, tx, layer, renameMoves(mappings)); err != nil {
			return 0, err
		}
	}
	return len(moves), nil
}

// planOverlayRenameTx resolves rename sources, validates the destination on the
// merged view and in each source's physical project, and returns the moves plus
// the files an overwriting file rename must delete first.
func (s *Service) planOverlayRenameTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, fromPath, toPath string, overwrite bool) (
	[]overlayRenameMove, []layerFile, error,
) {
	sources, sourceIsDirectory, err := s.overlayRenameSourcesTx(ctx, tx, apiKeyHash, project, fromPath)
	if err != nil {
		return nil, nil, err
	}
	if sourceIsDirectory && strings.HasPrefix(toPath, fromPath+"/") {
		return nil, nil, NewError(ErrCodeInvalidPath, "destination cannot be within source subtree", false)
	}
	blocked, err := s.visibleFileAtAncestor(ctx, tx, apiKeyHash, project, toPath)
	if err != nil {
		return nil, nil, err
	}
	if blocked {
		return nil, nil, NewError(ErrCodeNotDirectory, "parent path is a file", false)
	}
	sourceIDs := make(map[uint64]struct{}, len(sources))
	moves := make([]overlayRenameMove, 0, len(sources))
	for _, source := range sources {
		sourceIDs[source.ID] = struct{}{}
		newPath := toPath
		if sourceIsDirectory {
			newPath = toPath + strings.TrimPrefix(source.Path, fromPath)
		}
		moves = append(moves, overlayRenameMove{
			renameMapping: renameMapping{ID: source.ID, OldPath: source.Path, NewPath: newPath},
			Project:       source.Project,
		})
	}

	overwritten, err := s.overlayRenameMergedConflictsTx(ctx, tx, apiKeyHash, project, toPath, moves, sourceIDs, sourceIsDirectory, overwrite)
	if err != nil {
		return nil, nil, err
	}
	hidden, err := s.overlayRenamePhysicalConflictsTx(ctx, tx, apiKeyHash, toPath, moves, sourceIDs, overwritten, sourceIsDirectory, overwrite)
	if err != nil {
		return nil, nil, err
	}
	return moves, append(overwritten, hidden...), nil
}

// overlayRenameSourcesTx resolves the visible file or directory subtree at fromPath.
func (s *Service) overlayRenameSourcesTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, fromPath string) ([]layerFile, bool, error) {
	file, err := s.resolveActiveFile(ctx, tx, apiKeyHash, project, fromPath)
	if err == nil {
		return []layerFile{{ID: file.ID, Project: file.Project, Path: file.Path}}, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, errors.Wrap(err, "query rename source file")
	}
	visible, err := s.visibleDescendants(ctx, tx, apiKeyHash, project, fromPath)
	if err != nil {
		return nil, false, err
	}
	if len(visible) == 0 {
		return nil, false, NewError(ErrCodeNotFound, "source path not found", false)
	}
	return visible, true, nil
}

// overlayRenameMergedConflictsTx checks the destination against the merged view.
// For an overwriting file rename it returns the visible destination file when it
// must be deleted: a project file always (it would shadow the moved file), a
// default file only when the source is a default too (same physical name).
func (s *Service) overlayRenameMergedConflictsTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, toPath string,
	moves []overlayRenameMove, sourceIDs map[uint64]struct{}, sourceIsDirectory, overwrite bool,
) ([]layerFile, error) {
	destination, err := s.resolveActiveFile(ctx, tx, apiKeyHash, project, toPath)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, errors.Wrap(err, "query rename destination")
	}
	below, err := s.visibleDescendants(ctx, tx, apiKeyHash, project, toPath)
	if err != nil {
		return nil, err
	}
	if !sourceIsDirectory {
		// Like the physical rename, the source itself counts: a file cannot
		// replace the directory that contains it.
		if len(below) > 0 {
			return nil, NewError(ErrCodeAlreadyExists, "destination path already exists", false)
		}
		if destination == nil {
			return nil, nil
		}
		if _, isSource := sourceIDs[destination.ID]; isSource {
			return nil, nil
		}
		if !overwrite {
			return nil, NewError(ErrCodeAlreadyExists, "destination path already exists", false)
		}
		if destination.Project == project || destination.Project == moves[0].Project {
			return []layerFile{{ID: destination.ID, Project: destination.Project, Path: destination.Path}}, nil
		}
		// A project file moved over a default shadows it; the default stays global.
		return nil, nil
	}
	if destination != nil {
		return nil, NewError(ErrCodeAlreadyExists, "destination path already exists", false)
	}
	occupied := make([]string, 0, len(below))
	for _, file := range below {
		if _, isSource := sourceIDs[file.ID]; !isSource {
			occupied = append(occupied, file.Path)
		}
	}
	shadow := newUpperShadow(occupied)
	for _, move := range moves {
		if shadow.hides(move.NewPath) {
			return nil, NewError(ErrCodeAlreadyExists, "destination path collision", false)
		}
	}
	return nil, nil
}

// overlayRenamePhysicalConflictsTx keeps every physical project consistent: a
// moved file must not land on, above, or below a file of its own project that
// the merged view hides. An overwriting file rename deletes a hidden same-name
// file; any other hidden collision is rejected.
func (s *Service) overlayRenamePhysicalConflictsTx(ctx context.Context, tx *sql.Tx, apiKeyHash, toPath string,
	moves []overlayRenameMove, sourceIDs map[uint64]struct{}, overwritten []layerFile, sourceIsDirectory, overwrite bool,
) ([]layerFile, error) {
	removed := make(map[uint64]struct{}, len(overwritten))
	for _, file := range overwritten {
		removed[file.ID] = struct{}{}
	}
	layers := make(map[string][]overlayRenameMove)
	for _, move := range moves {
		layers[move.Project] = append(layers[move.Project], move)
	}
	var hidden []layerFile
	for layer, layerMoves := range layers {
		blocked, err := s.upperFileAtOrAbove(ctx, tx, apiKeyHash, layer, parentPath(toPath))
		if err != nil {
			return nil, err
		}
		if blocked {
			return nil, NewError(ErrCodeNotDirectory, "parent path is a file in the default layer", false)
		}
		occupied, err := s.layerDescendants(ctx, tx, apiKeyHash, layer, toPath)
		if err != nil {
			return nil, err
		}
		if atDestination, findErr := s.findActiveFileQ(ctx, tx, apiKeyHash, layer, toPath); findErr == nil {
			occupied = append(occupied, layerFile{ID: atDestination.ID, Project: layer, Path: atDestination.Path})
		} else if !errors.Is(findErr, sql.ErrNoRows) {
			return nil, errors.Wrap(findErr, "query physical rename destination")
		}
		var remaining []string
		for _, file := range occupied {
			_, isSource := sourceIDs[file.ID]
			_, isRemoved := removed[file.ID]
			if isSource || isRemoved {
				continue
			}
			if !sourceIsDirectory && overwrite && file.Path == toPath {
				hidden = append(hidden, file)
				continue
			}
			remaining = append(remaining, file.Path)
		}
		shadow := newUpperShadow(remaining)
		for _, move := range layerMoves {
			if shadow.hides(move.NewPath) {
				return nil, NewError(ErrCodeAlreadyExists, "destination path already exists", false)
			}
		}
	}
	return hidden, nil
}

// parentPath returns the immediate parent of path, or "" for a top-level path.
func parentPath(path string) string {
	if idx := strings.LastIndex(path, "/"); idx > 0 {
		return path[:idx]
	}
	return ""
}
