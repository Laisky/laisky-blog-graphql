package fileio

import (
	"context"
	"time"

	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	"github.com/Laisky/laisky-blog-graphql/library"
)

// MutationResolver implements the mutating FileIO and memory GraphQL fields.
type MutationResolver struct{ *Resolver }

// FileWrite commits content and returns the version captured inside the
// mutation transaction. The shared gate rejects a call with neither
// expected_version nor create_only, so GraphQL cannot perform a blind write.
func (r *MutationResolver) FileWrite(ctx context.Context, project, path, content, contentEncoding string,
	offset *library.BigInt, mode models.FileIOWriteMode, expectedVersion *string, createOnly *bool,
	plugin *models.MemoryPlugin,
) (*models.FileIOWriteResult, error) {
	if r.plugin == nil {
		return nil, unavailable("file_write")
	}
	auth, err := r.authorize(ctx)
	if err != nil {
		return nil, err
	}
	path = normalizePath(path)
	if err := validateTarget(project, path); err != nil {
		return nil, err
	}
	conditions, err := preconditions(expectedVersion, createOnly)
	if err != nil {
		return nil, err
	}
	writeOffset := int64(0)
	if offset != nil {
		writeOffset = offset.Int64()
	}
	svc, writeCtx, err := r.conditional(withPlugin(ctx, plugin), auth, project, path, "", files.FileOperationWrite, conditions)
	if err != nil {
		return nil, err
	}
	startAt := time.Now().UTC()
	log := logger(ctx, "file_write").With(zap.String(fieldProject, project))
	result, writeErr := svc.Write(writeCtx, auth, project, path, content, contentEncoding, writeOffset, writeMode(mode))
	r.record(ctx, log, auth, "file_write", map[string]any{fieldProject: project, fieldPath: path,
		"content": content, "mode": string(writeMode(mode)), "offset": writeOffset}, startAt, writeErr)
	if writeErr != nil {
		return nil, graphQLError(writeErr, "write file")
	}
	return &models.FileIOWriteResult{BytesWritten: library.NewBigInt(result.BytesWritten),
		Version: optionalString(result.Version)}, nil
}

// FileDelete removes a file, or a subtree when recursive. The shared gate
// requires expected_version and refuses to delete the project root.
func (r *MutationResolver) FileDelete(ctx context.Context, project, path string, recursive bool,
	expectedVersion string, plugin *models.MemoryPlugin,
) (*models.FileIODeleteResult, error) {
	if r.plugin == nil {
		return nil, unavailable("file_delete")
	}
	auth, err := r.authorize(ctx)
	if err != nil {
		return nil, err
	}
	path = normalizePath(path)
	if err := validateTarget(project, path); err != nil {
		return nil, err
	}
	conditions, err := preconditions(&expectedVersion, nil)
	if err != nil {
		return nil, err
	}
	svc, deleteCtx, err := r.conditional(withPlugin(ctx, plugin), auth, project, path, "", files.FileOperationDelete, conditions)
	if err != nil {
		return nil, err
	}
	startAt := time.Now().UTC()
	log := logger(ctx, "file_delete").With(zap.String(fieldProject, project))
	result, deleteErr := svc.Delete(deleteCtx, auth, project, path, recursive)
	r.record(ctx, log, auth, "file_delete", map[string]any{fieldProject: project, fieldPath: path,
		"recursive": recursive}, startAt, deleteErr)
	if deleteErr != nil {
		return nil, graphQLError(deleteErr, "delete file")
	}
	return &models.FileIODeleteResult{DeletedCount: result.DeletedCount}, nil
}

// FileRename moves a file or a directory subtree. Destination conditions are
// validated by the shared gate and are mutually exclusive.
func (r *MutationResolver) FileRename(ctx context.Context, project, fromPath, toPath string, overwrite bool,
	expectedVersion string, expectedDestinationVersion *string, destinationMustNotExist *bool,
	plugin *models.MemoryPlugin,
) (*models.FileIORenameResult, error) {
	if r.plugin == nil {
		return nil, unavailable("file_rename")
	}
	auth, err := r.authorize(ctx)
	if err != nil {
		return nil, err
	}
	fromPath, toPath = normalizePath(fromPath), normalizePath(toPath)
	if err := validateTarget(project, fromPath); err != nil {
		return nil, err
	}
	if err := files.ValidatePath(toPath); err != nil {
		return nil, graphQLError(err, "validate destination path")
	}
	conditions, err := preconditions(&expectedVersion, nil)
	if err != nil {
		return nil, err
	}
	if expectedDestinationVersion != nil {
		if *expectedDestinationVersion == "" {
			return nil, invalidArgument("expected_destination_version must be a non-empty opaque version string")
		}
		if err := files.ValidateFileVersion(*expectedDestinationVersion); err != nil {
			return nil, graphQLError(err, "validate expected_destination_version")
		}
		conditions.ExpectedDestinationVersion = *expectedDestinationVersion
	}
	if destinationMustNotExist != nil {
		conditions.DestinationMustNotExist = *destinationMustNotExist
	}
	svc, renameCtx, err := r.conditional(withPlugin(ctx, plugin), auth, project, fromPath, toPath,
		files.FileOperationRename, conditions)
	if err != nil {
		return nil, err
	}
	startAt := time.Now().UTC()
	log := logger(ctx, "file_rename").With(zap.String(fieldProject, project))
	result, renameErr := svc.Rename(renameCtx, auth, project, fromPath, toPath, overwrite)
	r.record(ctx, log, auth, "file_rename", map[string]any{fieldProject: project, "from_path": fromPath,
		"to_path": toPath, "overwrite": overwrite}, startAt, renameErr)
	if renameErr != nil {
		return nil, graphQLError(renameErr, "rename file")
	}
	return &models.FileIORenameResult{MovedCount: result.MovedCount}, nil
}

// FileRestoreVersion writes immutable historical bytes back as a new live
// revision. history_id selects the bytes; the live-file precondition is still
// mandatory and is checked against the CURRENT file, not the snapshot.
func (r *MutationResolver) FileRestoreVersion(ctx context.Context, project, path, historyID string,
	expectedVersion *string, createOnly *bool, plugin *models.MemoryPlugin,
) (*models.FileIOWriteResult, error) {
	if r.plugin == nil || r.history == nil {
		return nil, unavailable("file_restore_version")
	}
	auth, err := r.authorize(ctx)
	if err != nil {
		return nil, err
	}
	path = normalizePath(path)
	if err := validateTarget(project, path); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, graphQLError(files.NewError(files.ErrCodeInvalidPath,
			"history requires an exact file path", false), "validate history path")
	}
	id, err := files.ParseHistoryID(historyID)
	if err != nil {
		return nil, graphQLError(err, "validate history id")
	}
	conditions, err := preconditions(expectedVersion, createOnly)
	if err != nil {
		return nil, err
	}
	// The protected operation is the selected plugin's write, not a preflight
	// stat and not the immutable history row.
	svc, writeCtx, err := r.conditional(withPlugin(ctx, plugin), auth, project, path, "",
		files.FileOperationWrite, conditions)
	if err != nil {
		return nil, err
	}
	startAt := time.Now().UTC()
	log := logger(ctx, "file_restore_version").With(zap.String(fieldProject, project))
	snapshot, readErr := r.history.ReadVersion(writeCtx, auth, project, path, id)
	if readErr != nil {
		r.record(ctx, log, auth, "file_restore_version", map[string]any{fieldProject: project, fieldPath: path,
			fieldHistoryID: historyID}, startAt, readErr)
		return nil, graphQLError(readErr, "read file version")
	}
	result, writeErr := svc.Write(writeCtx, auth, project, path, string(snapshot.Content), "utf-8", 0, files.WriteModeTruncate)
	r.record(ctx, log, auth, "file_restore_version", map[string]any{fieldProject: project, fieldPath: path,
		fieldHistoryID: historyID}, startAt, writeErr)
	if writeErr != nil {
		return nil, graphQLError(writeErr, "restore file version")
	}
	return &models.FileIOWriteResult{BytesWritten: library.NewBigInt(result.BytesWritten),
		Version: optionalString(result.Version)}, nil
}

// MemoryBeforeTurn prepares model input with recalled memory context.
func (r *MutationResolver) MemoryBeforeTurn(ctx context.Context, input models.MemoryBeforeTurnInput,
) (*models.MemoryBeforeTurnResult, error) {
	if r.memory == nil {
		return nil, unavailable("memory_before_turn")
	}
	auth, err := r.authorize(ctx)
	if err != nil {
		return nil, err
	}
	request, err := beforeTurnRequest(input)
	if err != nil {
		return nil, err
	}
	startAt := time.Now().UTC()
	log := logger(ctx, "memory_before_turn").With(zap.String(fieldProject, request.Project),
		zap.String("session_id", request.SessionID), zap.String("turn_id", request.TurnID))
	response, turnErr := r.memory.BeforeTurn(withPlugin(ctx, input.Plugin), auth, request)
	r.record(ctx, log, auth, "memory_before_turn", memoryAuditParameters(request.Project, request.SessionID,
		request.TurnID), startAt, turnErr)
	if turnErr != nil {
		return nil, graphQLError(turnErr, "prepare memory turn")
	}
	return &models.MemoryBeforeTurnResult{
		InputItems:        responseItems(response.InputItems),
		RecallFactIds:     stringsOrEmpty(response.RecallFactIDs),
		RecallInsightIds:  stringsOrEmpty(response.RecallInsightIDs),
		ContextTokenCount: response.ContextTokenCount,
	}, nil
}

// MemoryAfterTurn persists turn artifacts after the model response.
func (r *MutationResolver) MemoryAfterTurn(ctx context.Context, input models.MemoryAfterTurnInput,
) (*models.MemoryAck, error) {
	if r.memory == nil {
		return nil, unavailable("memory_after_turn")
	}
	auth, err := r.authorize(ctx)
	if err != nil {
		return nil, err
	}
	request := afterTurnRequest(input)
	startAt := time.Now().UTC()
	log := logger(ctx, "memory_after_turn").With(zap.String(fieldProject, request.Project),
		zap.String("session_id", request.SessionID), zap.String("turn_id", request.TurnID))
	turnErr := r.memory.AfterTurn(withPlugin(ctx, input.Plugin), auth, request)
	r.record(ctx, log, auth, "memory_after_turn", memoryAuditParameters(request.Project, request.SessionID,
		request.TurnID), startAt, turnErr)
	if turnErr != nil {
		return nil, graphQLError(turnErr, "persist memory turn")
	}
	return &models.MemoryAck{Ok: true}, nil
}

// MemoryRunMaintenance runs compaction, retention and summary refresh for one session.
func (r *MutationResolver) MemoryRunMaintenance(ctx context.Context, project, sessionID *string,
	plugin *models.MemoryPlugin,
) (*models.MemoryAck, error) {
	if r.memory == nil {
		return nil, unavailable("memory_run_maintenance")
	}
	auth, err := r.authorize(ctx)
	if err != nil {
		return nil, err
	}
	request := sessionRequest(project, sessionID)
	startAt := time.Now().UTC()
	log := logger(ctx, "memory_run_maintenance").With(zap.String(fieldProject, request.Project),
		zap.String("session_id", request.SessionID))
	maintenanceErr := r.memory.RunMaintenance(withPlugin(ctx, plugin), auth, request)
	r.record(ctx, log, auth, "memory_run_maintenance", memoryAuditParameters(request.Project,
		request.SessionID, ""), startAt, maintenanceErr)
	if maintenanceErr != nil {
		return nil, graphQLError(maintenanceErr, "run memory maintenance")
	}
	return &models.MemoryAck{Ok: true}, nil
}

// writeMode maps the GraphQL enum onto the shared write mode.
func writeMode(mode models.FileIOWriteMode) files.WriteMode {
	switch mode {
	case models.FileIOWriteModeOverwrite:
		return files.WriteModeOverwrite
	case models.FileIOWriteModeTruncate:
		return files.WriteModeTruncate
	case models.FileIOWriteModeAppend:
		return files.WriteModeAppend
	default:
		return files.WriteModeAppend
	}
}
