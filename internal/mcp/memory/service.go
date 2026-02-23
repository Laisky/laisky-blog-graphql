package memory

import (
	"context"
	"database/sql"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// Service orchestrates MCP-native memory lifecycle operations.
type Service struct {
	db          *sql.DB
	isPostgres  bool
	fileService *files.Service
	settings    Settings
	logger      logSDK.Logger
	clock       func() time.Time
}

// sqlExecutor abstracts SQL execution over either *sql.DB or *sql.Tx.
type sqlExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// chooseExecutor returns tx when present, otherwise db.
func chooseExecutor(tx *sql.Tx, db *sql.DB) sqlExecutor {
	if tx != nil {
		return tx
	}
	return db
}

// NewService creates a memory lifecycle service.
func NewService(db *sql.DB, fileService *files.Service, settings Settings, logger logSDK.Logger, clock func() time.Time) (*Service, error) {
	if db == nil {
		return nil, errors.WithStack(NewError(ErrCodeInternal, "sql db is required", false))
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
		isPostgres:  isPostgresDB(db),
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

	err := withSessionLock(ctx, service.db, auth.APIKeyHash, request.Project, request.SessionID, service.settings.SessionLockTimeout, func(tx *sql.Tx) error {
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

	err := withSessionLock(ctx, service.db, auth.APIKeyHash, request.Project, request.SessionID, service.settings.SessionLockTimeout, func(tx *sql.Tx) error {
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
func (service *Service) claimAfterTurnGuard(ctx context.Context, tx *sql.Tx, auth files.AuthContext, request AfterTurnRequest) (bool, error) {
	now := service.clock().UTC()
	executor := chooseExecutor(tx, service.db)
	insertQuery := "INSERT INTO turn_guards (api_key_hash, project, session_id, turn_id, status, updated_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)"
	if service.isPostgres {
		insertQuery = "INSERT INTO turn_guards (api_key_hash, project, session_id, turn_id, status, updated_at, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7)"
	}

	_, createErr := executor.ExecContext(ctx, insertQuery,
		auth.APIKeyHash,
		request.Project,
		request.SessionID,
		request.TurnID,
		turnGuardStatusProcessing,
		now,
		now,
	)
	if createErr != nil {
		if !isUniqueConstraintError(createErr) {
			return false, errors.Wrap(createErr, "create turn guard")
		}

		selectQuery := "SELECT id, status, updated_at FROM turn_guards WHERE api_key_hash = ? AND project = ? AND session_id = ? AND turn_id = ?"
		if service.isPostgres {
			selectQuery = "SELECT id, status, updated_at FROM turn_guards WHERE api_key_hash = $1 AND project = $2 AND session_id = $3 AND turn_id = $4"
		}

		existing := TurnGuard{}
		findErr := executor.QueryRowContext(ctx, selectQuery, auth.APIKeyHash, request.Project, request.SessionID, request.TurnID).
			Scan(&existing.ID, &existing.Status, &existing.UpdatedAt)
		if findErr != nil {
			if errors.Is(findErr, sql.ErrNoRows) {
				return false, errors.WithStack(NewError(ErrCodeInternal, "turn guard disappeared after unique conflict", false))
			}
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

		updateQuery := "UPDATE turn_guards SET status = ?, updated_at = ? WHERE id = ?"
		if service.isPostgres {
			updateQuery = "UPDATE turn_guards SET status = $1, updated_at = $2 WHERE id = $3"
		}

		_, updateErr := executor.ExecContext(ctx, updateQuery, turnGuardStatusProcessing, now, existing.ID)
		if updateErr != nil {
			return false, errors.Wrap(updateErr, "refresh stale turn guard")
		}
	}

	return true, nil
}

// markAfterTurnDone marks a processed turn as completed.
func (service *Service) markAfterTurnDone(ctx context.Context, tx *sql.Tx, auth files.AuthContext, request AfterTurnRequest) error {
	executor := chooseExecutor(tx, service.db)
	updateQuery := "UPDATE turn_guards SET status = ?, updated_at = ? WHERE api_key_hash = ? AND project = ? AND session_id = ? AND turn_id = ?"
	if service.isPostgres {
		updateQuery = "UPDATE turn_guards SET status = $1, updated_at = $2 WHERE api_key_hash = $3 AND project = $4 AND session_id = $5 AND turn_id = $6"
	}

	_, updateErr := executor.ExecContext(ctx, updateQuery,
		turnGuardStatusDone,
		service.clock().UTC(),
		auth.APIKeyHash,
		request.Project,
		request.SessionID,
		request.TurnID,
	)
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
