package memory

import (
	"context"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"gorm.io/gorm"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// Service orchestrates MCP-native memory lifecycle operations.
type Service struct {
	db          *gorm.DB
	fileService *files.Service
	settings    Settings
	logger      logSDK.Logger
	clock       func() time.Time
}

// NewService creates a memory lifecycle service.
func NewService(db *gorm.DB, fileService *files.Service, settings Settings, logger logSDK.Logger, clock func() time.Time) (*Service, error) {
	if db == nil {
		return nil, errors.WithStack(NewError(ErrCodeInternal, "gorm db is required", false))
	}
	if fileService == nil {
		return nil, errors.WithStack(NewError(ErrCodeInternal, "file service is required", false))
	}
	if logger == nil {
		logger = log.Logger.Named("mcp_memory")
	}
	if clock == nil {
		clock = func() time.Time {
			return time.Now().UTC()
		}
	}

	if err := runMigrations(context.Background(), db); err != nil {
		return nil, errors.WithStack(err)
	}

	return &Service{
		db:          db,
		fileService: fileService,
		settings:    settings,
		logger:      logger,
		clock:       clock,
	}, nil
}

// BeforeTurn prepares model input by recalling memory facts and recent context.
func (service *Service) BeforeTurn(ctx context.Context, auth files.AuthContext, request BeforeTurnRequest) (BeforeTurnResponse, error) {
	if err := validateBeforeTurnRequest(auth, request); err != nil {
		return BeforeTurnResponse{}, errors.WithStack(err)
	}

	engine, err := service.newEngineForAuth(auth)
	if err != nil {
		return BeforeTurnResponse{}, errors.WithStack(err)
	}

	output, err := engine.BeforeTurn(ctx, toSDKBeforeTurnInput(request))
	if err != nil {
		return BeforeTurnResponse{}, errors.Wrap(err, "run before turn")
	}

	return BeforeTurnResponse{
		InputItems:        output.InputItems,
		RecallFactIDs:     output.RecallFactIDs,
		ContextTokenCount: output.ContextTokenCount,
	}, nil
}

// AfterTurn persists the turn output with idempotency guard and session serialization.
func (service *Service) AfterTurn(ctx context.Context, auth files.AuthContext, request AfterTurnRequest) error {
	if err := validateAfterTurnRequest(auth, request); err != nil {
		return errors.WithStack(err)
	}

	err := withSessionLock(ctx, service.db, auth.APIKeyHash, request.Project, request.SessionID, service.settings.SessionLockTimeout, func(tx *gorm.DB) error {
		claimed, claimErr := service.claimAfterTurnGuard(ctx, tx, auth, request)
		if claimErr != nil {
			return claimErr
		}
		if !claimed {
			return nil
		}

		engine, engineErr := service.newEngineForAuth(auth)
		if engineErr != nil {
			return errors.WithStack(engineErr)
		}

		if runErr := engine.AfterTurn(ctx, toSDKAfterTurnInput(request)); runErr != nil {
			return errors.Wrap(runErr, "run after turn")
		}

		if doneErr := service.markAfterTurnDone(ctx, tx, auth, request); doneErr != nil {
			return errors.WithStack(doneErr)
		}

		return nil
	})
	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// RunMaintenance runs compaction and retention cleanup for one session.
func (service *Service) RunMaintenance(ctx context.Context, auth files.AuthContext, request SessionRequest) error {
	if err := validateSessionRequest(auth, request); err != nil {
		return errors.WithStack(err)
	}

	err := withSessionLock(ctx, service.db, auth.APIKeyHash, request.Project, request.SessionID, service.settings.SessionLockTimeout, func(tx *gorm.DB) error {
		_ = tx
		engine, engineErr := service.newEngineForAuth(auth)
		if engineErr != nil {
			return errors.WithStack(engineErr)
		}

		if runErr := engine.RunMaintenance(ctx, request.Project, request.SessionID); runErr != nil {
			return errors.Wrap(runErr, "run maintenance")
		}

		return nil
	})
	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// ListDirWithAbstract lists memory directories enriched by abstract metadata.
func (service *Service) ListDirWithAbstract(ctx context.Context, auth files.AuthContext, request ListDirWithAbstractRequest) (ListDirWithAbstractResponse, error) {
	if err := validateListDirRequest(auth, request); err != nil {
		return ListDirWithAbstractResponse{}, errors.WithStack(err)
	}

	engine, err := service.newEngineForAuth(auth)
	if err != nil {
		return ListDirWithAbstractResponse{}, errors.WithStack(err)
	}

	summaries, err := engine.ListDirWithAbstract(ctx, request.Project, request.SessionID, request.Path, request.Depth, request.Limit)
	if err != nil {
		return ListDirWithAbstractResponse{}, errors.Wrap(err, "list dir with abstract")
	}

	return ListDirWithAbstractResponse{Summaries: summaries}, nil
}

// claimAfterTurnGuard marks a turn as processing and enforces idempotency.
func (service *Service) claimAfterTurnGuard(ctx context.Context, tx *gorm.DB, auth files.AuthContext, request AfterTurnRequest) (bool, error) {
	now := service.clock().UTC()
	guard := TurnGuard{
		APIKeyHash: auth.APIKeyHash,
		Project:    request.Project,
		SessionID:  request.SessionID,
		TurnID:     request.TurnID,
		Status:     turnGuardStatusProcessing,
		UpdatedAt:  now,
		CreatedAt:  now,
	}

	createErr := tx.WithContext(ctx).Create(&guard).Error
	if createErr != nil {
		if !isUniqueConstraintError(createErr) {
			return false, errors.Wrap(createErr, "create turn guard")
		}

		existing := TurnGuard{}
		findErr := tx.WithContext(ctx).
			Where("api_key_hash = ? AND project = ? AND session_id = ? AND turn_id = ?", auth.APIKeyHash, request.Project, request.SessionID, request.TurnID).
			First(&existing).Error
		if findErr != nil {
			return false, errors.Wrap(findErr, "query existing turn guard")
		}

		if existing.Status == turnGuardStatusDone {
			return false, nil
		}

		if existing.Status == turnGuardStatusProcessing {
			freshDeadline := existing.UpdatedAt.UTC().Add(2 * time.Minute)
			if freshDeadline.After(now) {
				return false, errors.WithStack(NewError(ErrCodeResourceBusy, "resource busy", true))
			}
		}

		updateErr := tx.WithContext(ctx).Model(&TurnGuard{}).
			Where("id = ?", existing.ID).
			Updates(map[string]any{"status": turnGuardStatusProcessing, "updated_at": now}).Error
		if updateErr != nil {
			return false, errors.Wrap(updateErr, "refresh stale turn guard")
		}
	}

	return true, nil
}

// markAfterTurnDone marks a processed turn as completed.
func (service *Service) markAfterTurnDone(ctx context.Context, tx *gorm.DB, auth files.AuthContext, request AfterTurnRequest) error {
	updateErr := tx.WithContext(ctx).Model(&TurnGuard{}).
		Where("api_key_hash = ? AND project = ? AND session_id = ? AND turn_id = ?", auth.APIKeyHash, request.Project, request.SessionID, request.TurnID).
		Updates(map[string]any{"status": turnGuardStatusDone, "updated_at": service.clock().UTC()}).Error
	if updateErr != nil {
		return errors.Wrap(updateErr, "mark turn guard done")
	}
	return nil
}

// validateBeforeTurnRequest validates memory_before_turn inputs.
func validateBeforeTurnRequest(auth files.AuthContext, request BeforeTurnRequest) error {
	if strings.TrimSpace(auth.APIKeyHash) == "" {
		return NewError(ErrCodePermissionDenied, "missing authorization", false)
	}
	if strings.TrimSpace(request.Project) == "" {
		return NewError(ErrCodeInvalidArgument, "project is required", false)
	}
	if strings.TrimSpace(request.SessionID) == "" {
		return NewError(ErrCodeInvalidArgument, "session_id is required", false)
	}
	if strings.TrimSpace(request.TurnID) == "" {
		return NewError(ErrCodeInvalidArgument, "turn_id is required", false)
	}
	return nil
}

// validateAfterTurnRequest validates memory_after_turn inputs.
func validateAfterTurnRequest(auth files.AuthContext, request AfterTurnRequest) error {
	if strings.TrimSpace(auth.APIKeyHash) == "" {
		return NewError(ErrCodePermissionDenied, "missing authorization", false)
	}
	if strings.TrimSpace(request.Project) == "" {
		return NewError(ErrCodeInvalidArgument, "project is required", false)
	}
	if strings.TrimSpace(request.SessionID) == "" {
		return NewError(ErrCodeInvalidArgument, "session_id is required", false)
	}
	if strings.TrimSpace(request.TurnID) == "" {
		return NewError(ErrCodeInvalidArgument, "turn_id is required", false)
	}
	return nil
}

// validateSessionRequest validates maintenance/session-scope request inputs.
func validateSessionRequest(auth files.AuthContext, request SessionRequest) error {
	if strings.TrimSpace(auth.APIKeyHash) == "" {
		return NewError(ErrCodePermissionDenied, "missing authorization", false)
	}
	if strings.TrimSpace(request.Project) == "" {
		return NewError(ErrCodeInvalidArgument, "project is required", false)
	}
	if strings.TrimSpace(request.SessionID) == "" {
		return NewError(ErrCodeInvalidArgument, "session_id is required", false)
	}
	return nil
}

// validateListDirRequest validates list_dir_with_abstract request inputs.
func validateListDirRequest(auth files.AuthContext, request ListDirWithAbstractRequest) error {
	if err := validateSessionRequest(auth, SessionRequest{Project: request.Project, SessionID: request.SessionID}); err != nil {
		return err
	}
	if request.Depth < 0 {
		return NewError(ErrCodeInvalidArgument, "depth must be >= 0", false)
	}
	if request.Limit <= 0 {
		return NewError(ErrCodeInvalidArgument, "limit must be > 0", false)
	}
	return nil
}
