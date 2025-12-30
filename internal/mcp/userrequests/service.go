package userrequests

import (
	"context"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// Clock returns the current UTC time. Tests can replace it for determinism.
type Clock func() time.Time

// Service provides persistence helpers for MCP user requests.
type Service struct {
	db       *gorm.DB
	logger   logSDK.Logger
	clock    Clock
	settings Settings
}

const (
	defaultListLimit = 200
	maxTaskIDLength  = 255
)

// NewService constructs a Service backed by the provided gorm database.
func NewService(db *gorm.DB, logger logSDK.Logger, clock Clock, settings Settings) (*Service, error) {
	if db == nil {
		return nil, errors.New("gorm db is required")
	}
	if logger == nil {
		logger = log.Logger.Named("user_requests_service")
	}
	if clock == nil {
		clock = func() time.Time {
			return time.Now().UTC()
		}
	}
	if settings.RetentionDays <= 0 {
		settings = LoadSettingsFromConfig()
	}
	if settings.RetentionDays <= 0 {
		settings.RetentionDays = DefaultRetentionDays
	}
	if settings.RetentionSweepInterval <= 0 {
		settings.RetentionSweepInterval = DefaultRetentionSweepInterval
	}

	if err := db.AutoMigrate(&Request{}, &SavedCommand{}, &UserPreference{}); err != nil {
		return nil, errors.Wrap(err, "auto migrate mcp user requests tables")
	}

	return &Service{db: db, logger: logger, clock: clock, settings: settings}, nil
}

// CreateRequest stores a new user directive scoped to the provided authorization context.
func (s *Service) CreateRequest(ctx context.Context, auth *askuser.AuthorizationContext, content string, taskID string) (*Request, error) {
	if auth == nil {
		return nil, ErrInvalidAuthorization
	}
	body := strings.TrimSpace(content)
	if body == "" {
		return nil, ErrEmptyContent
	}

	taskID = normalizeTaskID(taskID)

	// Get the next sort order for pending requests
	var maxOrder int
	s.db.WithContext(ctx).
		Model(&Request{}).
		Where("api_key_hash = ? AND status = ?", auth.APIKeyHash, StatusPending).
		Select("COALESCE(MAX(sort_order), -1)").
		Row().
		Scan(&maxOrder)

	req := &Request{
		Content:      body,
		Status:       StatusPending,
		TaskID:       taskID,
		SortOrder:    maxOrder + 1,
		APIKeyHash:   auth.APIKeyHash,
		KeySuffix:    auth.KeySuffix,
		UserIdentity: auth.UserIdentity,
	}

	if err := s.db.WithContext(ctx).Create(req).Error; err != nil {
		return nil, errors.Wrap(err, "create user request")
	}

	s.log().Info("user request created",
		zap.String("request_id", req.ID.String()),
		zap.String("user", auth.UserIdentity),
		zap.String("task_id", req.TaskID),
	)

	return req, nil
}

// ListRequests returns pending and consumed entries for the authenticated user.
// Pending requests are returned in FIFO order (oldest first at top). When
// includeAllTasks is true, results span every task owned by the caller.
func (s *Service) ListRequests(ctx context.Context, auth *askuser.AuthorizationContext, taskID string, includeAllTasks bool) ([]Request, []Request, error) {
	if auth == nil {
		return nil, nil, ErrInvalidAuthorization
	}

	if err := s.pruneExpired(ctx); err != nil {
		return nil, nil, err
	}

	filteredTaskID := normalizeTaskID(taskID)
	pendingQuery := s.db.WithContext(ctx).
		Where("api_key_hash = ? AND status = ?", auth.APIKeyHash, StatusPending)
	consumedQuery := s.db.WithContext(ctx).
		Where("api_key_hash = ? AND status = ?", auth.APIKeyHash, StatusConsumed)

	if !includeAllTasks {
		pendingQuery = pendingQuery.Where("task_id = ?", filteredTaskID)
		consumedQuery = consumedQuery.Where("task_id = ?", filteredTaskID)
	}

	pending := make([]Request, 0)
	if err := pendingQuery.
		Order("sort_order ASC, created_at ASC").
		Limit(defaultListLimit).
		Find(&pending).Error; err != nil {
		return nil, nil, errors.Wrap(err, "list pending user requests")
	}

	consumed := make([]Request, 0)
	if err := consumedQuery.
		Order("consumed_at DESC, updated_at DESC").
		Limit(defaultListLimit).
		Find(&consumed).Error; err != nil {
		return nil, nil, errors.Wrap(err, "list consumed user requests")
	}

	logTaskID := filteredTaskID
	if includeAllTasks {
		logTaskID = "*"
	}
	s.log().Debug("listed user requests",
		zap.String("user", auth.UserIdentity),
		zap.Bool("all_tasks", includeAllTasks),
		zap.String("task_id", logTaskID),
		zap.Int("pending_count", len(pending)),
		zap.Int("consumed_count", len(consumed)),
	)

	return pending, consumed, nil
}

// ConsumeAllPending fetches all pending requests in FIFO order (oldest first) and atomically marks them as consumed.
// Returns the list of consumed requests or an empty slice if none are pending.
func (s *Service) ConsumeAllPending(ctx context.Context, auth *askuser.AuthorizationContext, taskID string) ([]Request, error) {
	if auth == nil {
		return nil, ErrInvalidAuthorization
	}

	taskID = normalizeTaskID(taskID)
	if err := s.pruneExpired(ctx); err != nil {
		return nil, err
	}

	// Fetch all pending requests in FIFO order (oldest first)
	var candidates []Request
	err := s.db.WithContext(ctx).
		Where("api_key_hash = ? AND task_id = ? AND status = ?", auth.APIKeyHash, taskID, StatusPending).
		Order("sort_order ASC, created_at ASC").
		Find(&candidates).Error
	if err != nil {
		return nil, errors.Wrap(err, "fetch pending user requests")
	}

	if len(candidates) == 0 {
		return nil, ErrNoPendingRequests
	}

	// Extract IDs for batch update
	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.ID.String()
	}

	now := s.clock()
	update := s.db.WithContext(ctx).
		Model(&Request{}).
		Where("id IN ? AND status = ?", ids, StatusPending).
		Updates(map[string]any{
			"status":      StatusConsumed,
			"consumed_at": now,
			"updated_at":  now,
		})
	if update.Error != nil {
		return nil, errors.Wrap(update.Error, "consume user requests")
	}

	// Update in-memory objects
	consumed := make([]Request, 0, len(candidates))
	for _, c := range candidates {
		c.Status = StatusConsumed
		c.ConsumedAt = &now
		c.UpdatedAt = now
		consumed = append(consumed, c)
	}

	return consumed, nil
}

// ConsumeFirstPending fetches only the oldest pending request (FIFO) and marks it as consumed.
// Returns the consumed request or ErrNoPendingRequests if none are pending.
func (s *Service) ConsumeFirstPending(ctx context.Context, auth *askuser.AuthorizationContext, taskID string) (*Request, error) {
	if auth == nil {
		return nil, ErrInvalidAuthorization
	}

	taskID = normalizeTaskID(taskID)
	if err := s.pruneExpired(ctx); err != nil {
		return nil, err
	}

	// Fetch the oldest pending request (FIFO)
	var candidate Request
	err := s.db.WithContext(ctx).
		Where("api_key_hash = ? AND task_id = ? AND status = ?", auth.APIKeyHash, taskID, StatusPending).
		Order("created_at ASC").
		First(&candidate).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNoPendingRequests
		}
		return nil, errors.Wrap(err, "fetch first pending user request")
	}

	now := s.clock()
	update := s.db.WithContext(ctx).
		Model(&Request{}).
		Where("id = ? AND status = ?", candidate.ID, StatusPending).
		Updates(map[string]any{
			"status":      StatusConsumed,
			"consumed_at": now,
			"updated_at":  now,
		})
	if update.Error != nil {
		return nil, errors.Wrap(update.Error, "consume first user request")
	}

	// Update in-memory object
	candidate.Status = StatusConsumed
	candidate.ConsumedAt = &now
	candidate.UpdatedAt = now

	return &candidate, nil
}

// ConsumeRequestByID marks a specific pending request as consumed.
// This is used when a command is sent directly to a waiting agent via the hold mechanism.
func (s *Service) ConsumeRequestByID(ctx context.Context, id uuid.UUID) error {
	now := s.clock()
	result := s.db.WithContext(ctx).
		Model(&Request{}).
		Where("id = ? AND status = ?", id, StatusPending).
		Updates(map[string]any{
			"status":      StatusConsumed,
			"consumed_at": now,
			"updated_at":  now,
		})
	if result.Error != nil {
		return errors.Wrap(result.Error, "consume request by id")
	}
	if result.RowsAffected == 0 {
		// Already consumed or not found - this is not an error in this context
		s.log().Debug("request already consumed or not found",
			zap.String("request_id", id.String()),
		)
	}
	return nil
}

// DeleteRequest removes a single request belonging to the authenticated user.
func (s *Service) DeleteRequest(ctx context.Context, auth *askuser.AuthorizationContext, id uuid.UUID, taskID string) error {
	if auth == nil {
		return ErrInvalidAuthorization
	}

	result := s.db.WithContext(ctx).
		Where("id = ? AND api_key_hash = ?", id, auth.APIKeyHash).
		Delete(&Request{})
	if result.Error != nil {
		return errors.Wrap(result.Error, "delete user request")
	}
	if result.RowsAffected == 0 {
		return ErrRequestNotFound
	}
	s.log().Debug("deleted user request",
		zap.String("user", auth.UserIdentity),
		zap.String("request_id", id.String()),
		zap.Int64("deleted", result.RowsAffected),
	)
	return nil
}

// DeleteAll removes requests tied to the authenticated user. When includeAllTasks is false,
// only the provided taskID is affected.
func (s *Service) DeleteAll(ctx context.Context, auth *askuser.AuthorizationContext, taskID string, includeAllTasks bool) (int64, error) {
	if auth == nil {
		return 0, ErrInvalidAuthorization
	}

	filteredTaskID := normalizeTaskID(taskID)
	query := s.db.WithContext(ctx).
		Where("api_key_hash = ?", auth.APIKeyHash)
	if !includeAllTasks {
		query = query.Where("task_id = ?", filteredTaskID)
	}

	result := query.Delete(&Request{})
	if result.Error != nil {
		return 0, errors.Wrap(result.Error, "delete all user requests")
	}
	logTaskID := filteredTaskID
	if includeAllTasks {
		logTaskID = "*"
	}
	s.log().Debug("deleted user requests",
		zap.String("user", auth.UserIdentity),
		zap.Bool("all_tasks", includeAllTasks),
		zap.String("task_id", logTaskID),
		zap.Int64("deleted", result.RowsAffected),
	)
	return result.RowsAffected, nil
}

// DeleteAllPending removes pending requests. When includeAllTasks is false the operation is
// restricted to the provided taskID.
func (s *Service) DeleteAllPending(ctx context.Context, auth *askuser.AuthorizationContext, taskID string, includeAllTasks bool) (int64, error) {
	if auth == nil {
		return 0, ErrInvalidAuthorization
	}

	filteredTaskID := normalizeTaskID(taskID)
	query := s.db.WithContext(ctx).
		Where("api_key_hash = ? AND status = ?", auth.APIKeyHash, StatusPending)
	if !includeAllTasks {
		query = query.Where("task_id = ?", filteredTaskID)
	}

	result := query.Delete(&Request{})
	if result.Error != nil {
		return 0, errors.Wrap(result.Error, "delete pending user requests")
	}
	logTaskID := filteredTaskID
	if includeAllTasks {
		logTaskID = "*"
	}
	s.log().Debug("deleted pending user requests",
		zap.String("user", auth.UserIdentity),
		zap.Bool("all_tasks", includeAllTasks),
		zap.String("task_id", logTaskID),
		zap.Int64("deleted", result.RowsAffected),
	)
	return result.RowsAffected, nil
}

func (s *Service) log() logSDK.Logger {
	if s != nil && s.logger != nil {
		return s.logger
	}
	return log.Logger.Named("user_requests_service")
}

func normalizeTaskID(input string) string {
	sanitized := rag.SanitizeTaskID(input)
	if sanitized == "" {
		sanitized = DefaultTaskID
	}
	if len(sanitized) > maxTaskIDLength {
		sanitized = sanitized[:maxTaskIDLength]
	}
	return sanitized
}

func (s *Service) pruneExpired(ctx context.Context) error {
	if s == nil || s.settings.RetentionDays <= 0 {
		return nil
	}
	cutoff := s.clock().AddDate(0, 0, -s.settings.RetentionDays)
	result := s.db.WithContext(ctx).
		Where("created_at < ?", cutoff).
		Delete(&Request{})
	if result.Error != nil {
		switch {
		case errors.Is(result.Error, context.Canceled), errors.Is(result.Error, context.DeadlineExceeded):
			s.log().Debug("prune expired aborted", zap.Error(result.Error))
			return nil
		default:
			return errors.Wrap(result.Error, "prune expired user requests")
		}
	}
	return nil
}

// DeleteConsumed removes consumed requests based on retention policies.
// If keepCount > 0, it retains the N most recent consumed requests.
// If keepDays > 0, it retains requests consumed within the last N days.
// If both are 0, it deletes all consumed requests. When includeAllTasks is false, only the
// provided taskID is considered.
func (s *Service) DeleteConsumed(ctx context.Context, auth *askuser.AuthorizationContext, keepCount int, keepDays int, taskID string, includeAllTasks bool) (int64, error) {
	if auth == nil {
		return 0, ErrInvalidAuthorization
	}

	filteredTaskID := normalizeTaskID(taskID)
	query := s.db.WithContext(ctx).
		Where("api_key_hash = ? AND status = ?", auth.APIKeyHash, StatusConsumed)
	if !includeAllTasks {
		query = query.Where("task_id = ?", filteredTaskID)
	}

	if keepCount > 0 {
		// Retain only the most recent N items.
		// We use a subquery to identify the IDs to keep.
		subQuery := s.db.Model(&Request{}).
			Select("id").
			Where("api_key_hash = ? AND status = ?", auth.APIKeyHash, StatusConsumed)
		if !includeAllTasks {
			subQuery = subQuery.Where("task_id = ?", filteredTaskID)
		}
		subQuery = subQuery.Order("consumed_at DESC").Limit(keepCount)

		query = query.Where("id NOT IN (?)", subQuery)
	} else if keepDays > 0 {
		// Retain items from the last N days.
		cutoff := s.clock().AddDate(0, 0, -keepDays)
		query = query.Where("consumed_at < ?", cutoff)
	}

	result := query.Delete(&Request{})
	if result.Error != nil {
		return 0, errors.Wrap(result.Error, "delete consumed requests")
	}
	logTaskID := filteredTaskID
	if includeAllTasks {
		logTaskID = "*"
	}
	s.log().Debug("deleted consumed user requests",
		zap.String("user", auth.UserIdentity),
		zap.Bool("all_tasks", includeAllTasks),
		zap.String("task_id", logTaskID),
		zap.Int64("deleted", result.RowsAffected),
	)
	return result.RowsAffected, nil
}

// ReorderRequests updates the sort order for multiple pending requests at once.
func (s *Service) ReorderRequests(ctx context.Context, auth *askuser.AuthorizationContext, orderedIDs []uuid.UUID) error {
	if auth == nil {
		return ErrInvalidAuthorization
	}

	if len(orderedIDs) == 0 {
		return nil
	}

	tx := s.db.WithContext(ctx).Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	for i, id := range orderedIDs {
		result := tx.Model(&Request{}).
			Where("id = ? AND api_key_hash = ? AND status = ?", id, auth.APIKeyHash, StatusPending).
			Update("sort_order", i)
		if result.Error != nil {
			tx.Rollback()
			return errors.Wrap(result.Error, "update sort order")
		}
	}

	if err := tx.Commit().Error; err != nil {
		return errors.Wrap(err, "commit reorder transaction")
	}

	s.log().Info("user requests reordered",
		zap.Int("count", len(orderedIDs)),
		zap.String("user", auth.UserIdentity),
	)

	return nil
}

// StartRetentionWorker launches a background pruner that periodically removes expired requests based on TTL settings.
// The worker stops when the provided context is canceled. When RetentionSweepInterval is zero, no worker is started.
func (s *Service) StartRetentionWorker(ctx context.Context) {
	if s == nil || s.settings.RetentionSweepInterval <= 0 {
		return
	}

	go func() {
		ticker := time.NewTicker(s.settings.RetentionSweepInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweepCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
				if err := s.pruneExpired(sweepCtx); err != nil {
					s.log().Error("prune expired user requests", zap.Error(err))
				}
				cancel()
			}
		}
	}()
}
