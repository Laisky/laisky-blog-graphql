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
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// Clock returns the current UTC time. Tests can replace it for determinism.
type Clock func() time.Time

// Service provides persistence helpers for MCP user requests.
type Service struct {
	db     *gorm.DB
	logger logSDK.Logger
	clock  Clock
}

const (
	defaultListLimit = 200
	maxTaskIDLength  = 255
)

// NewService constructs a Service backed by the provided gorm database.
func NewService(db *gorm.DB, logger logSDK.Logger, clock Clock) (*Service, error) {
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

	if err := db.AutoMigrate(&Request{}, &SavedCommand{}); err != nil {
		return nil, errors.Wrap(err, "auto migrate mcp user requests tables")
	}

	return &Service{db: db, logger: logger, clock: clock}, nil
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

	req := &Request{
		Content:      body,
		Status:       StatusPending,
		TaskID:       sanitizeTaskID(taskID),
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
func (s *Service) ListRequests(ctx context.Context, auth *askuser.AuthorizationContext) ([]Request, []Request, error) {
	if auth == nil {
		return nil, nil, ErrInvalidAuthorization
	}

	pending := make([]Request, 0)
	if err := s.db.WithContext(ctx).
		Where("api_key_hash = ? AND status = ?", auth.APIKeyHash, StatusPending).
		Order("created_at DESC").
		Limit(defaultListLimit).
		Find(&pending).Error; err != nil {
		return nil, nil, errors.Wrap(err, "list pending user requests")
	}

	consumed := make([]Request, 0)
	if err := s.db.WithContext(ctx).
		Where("api_key_hash = ? AND status = ?", auth.APIKeyHash, StatusConsumed).
		Order("consumed_at DESC, updated_at DESC").
		Limit(defaultListLimit).
		Find(&consumed).Error; err != nil {
		return nil, nil, errors.Wrap(err, "list consumed user requests")
	}

	return pending, consumed, nil
}

// ConsumeAllPending fetches all pending requests in FIFO order (oldest first) and atomically marks them as consumed.
// Returns the list of consumed requests or an empty slice if none are pending.
func (s *Service) ConsumeAllPending(ctx context.Context, auth *askuser.AuthorizationContext) ([]Request, error) {
	if auth == nil {
		return nil, ErrInvalidAuthorization
	}

	// Fetch all pending requests in FIFO order (oldest first)
	var candidates []Request
	err := s.db.WithContext(ctx).
		Where("api_key_hash = ? AND status = ?", auth.APIKeyHash, StatusPending).
		Order("created_at ASC").
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

// DeleteRequest removes a single request belonging to the authenticated user.
func (s *Service) DeleteRequest(ctx context.Context, auth *askuser.AuthorizationContext, id uuid.UUID) error {
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
	return nil
}

// DeleteAll removes every request tied to the authenticated user and returns the number of rows deleted.
func (s *Service) DeleteAll(ctx context.Context, auth *askuser.AuthorizationContext) (int64, error) {
	if auth == nil {
		return 0, ErrInvalidAuthorization
	}

	result := s.db.WithContext(ctx).
		Where("api_key_hash = ?", auth.APIKeyHash).
		Delete(&Request{})
	if result.Error != nil {
		return 0, errors.Wrap(result.Error, "delete all user requests")
	}
	return result.RowsAffected, nil
}

// DeleteAllPending removes only pending requests tied to the authenticated user and returns the number of rows deleted.
func (s *Service) DeleteAllPending(ctx context.Context, auth *askuser.AuthorizationContext) (int64, error) {
	if auth == nil {
		return 0, ErrInvalidAuthorization
	}

	result := s.db.WithContext(ctx).
		Where("api_key_hash = ? AND status = ?", auth.APIKeyHash, StatusPending).
		Delete(&Request{})
	if result.Error != nil {
		return 0, errors.Wrap(result.Error, "delete pending user requests")
	}
	return result.RowsAffected, nil
}

func (s *Service) log() logSDK.Logger {
	if s != nil && s.logger != nil {
		return s.logger
	}
	return log.Logger.Named("user_requests_service")
}

func sanitizeTaskID(input string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return DefaultTaskID
	}
	if len(trimmed) > maxTaskIDLength {
		return trimmed[:maxTaskIDLength]
	}
	return trimmed
}
