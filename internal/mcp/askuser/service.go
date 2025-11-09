package askuser

import (
	"context"
	"time"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"gorm.io/gorm"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

const (
	defaultPollInterval = time.Second
	defaultListLimit    = 50
	defaultHistoryLimit = 100
)

// Service provides persistence and coordination helpers for ask_user requests.
type Service struct {
	db     *gorm.DB
	logger logSDK.Logger
}

// NewService constructs the service and performs required migrations.
func NewService(db *gorm.DB, logger logSDK.Logger) (*Service, error) {
	if db == nil {
		return nil, errors.New("gorm db is required")
	}
	if logger == nil {
		logger = log.Logger.Named("ask_user_service")
	}

	if err := db.AutoMigrate(&Request{}); err != nil {
		return nil, errors.Wrap(err, "auto migrate ask_user tables")
	}

	return &Service{db: db, logger: logger}, nil
}

// CreateRequest persists a new question raised by an AI agent.
func (s *Service) CreateRequest(ctx context.Context, auth *AuthorizationContext, question string) (*Request, error) {
	if auth == nil {
		return nil, ErrInvalidAuthorization
	}
	req := &Request{
		Question:     question,
		Status:       StatusPending,
		APIKeyHash:   auth.APIKeyHash,
		KeySuffix:    auth.KeySuffix,
		UserIdentity: auth.UserIdentity,
		AIIdentity:   auth.AIIdentity,
	}

	if err := s.db.WithContext(ctx).Create(req).Error; err != nil {
		return nil, errors.Wrap(err, "create ask_user request")
	}
	s.log().Info("ask_user request created",
		zap.String("request_id", req.ID.String()),
		zap.String("user", auth.UserIdentity),
		zap.String("ai", auth.AIIdentity),
	)
	return req, nil
}

// WaitForAnswer blocks until the referenced request transitions to answered or the context is cancelled.
func (s *Service) WaitForAnswer(ctx context.Context, id uuid.UUID) (*Request, error) {
	ticker := time.NewTicker(defaultPollInterval)
	defer ticker.Stop()

	for {
		req, err := s.getByID(ctx, id)
		if err != nil {
			return nil, err
		}
		switch req.Status {
		case StatusAnswered:
			return req, nil
		case StatusCancelled, StatusExpired:
			return nil, errors.Errorf("request %s closed with status %s", id, req.Status)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// CancelRequest marks a request as cancelled when the caller stops waiting.
func (s *Service) CancelRequest(ctx context.Context, id uuid.UUID, status string) error {
	allowed := map[string]bool{StatusCancelled: true, StatusExpired: true}
	if !allowed[status] {
		status = StatusCancelled
	}
	return s.db.WithContext(ctx).Model(&Request{}).
		Where("id = ? AND status = ?", id, StatusPending).
		Updates(map[string]any{
			"status":     status,
			"updated_at": time.Now().UTC(),
		}).Error
}

// ListRequests returns pending and recent records for the provided authorization scope.
func (s *Service) ListRequests(ctx context.Context, auth *AuthorizationContext) ([]Request, []Request, error) {
	if auth == nil {
		return nil, nil, ErrInvalidAuthorization
	}

	pending := make([]Request, 0)
	if err := s.db.WithContext(ctx).
		Where("api_key_hash = ? AND status = ?", auth.APIKeyHash, StatusPending).
		Order("created_at ASC").
		Limit(defaultListLimit).
		Find(&pending).Error; err != nil {
		return nil, nil, errors.Wrap(err, "query pending requests")
	}

	history := make([]Request, 0)
	if err := s.db.WithContext(ctx).
		Where("api_key_hash = ? AND status <> ?", auth.APIKeyHash, StatusPending).
		Order("updated_at DESC").
		Limit(defaultHistoryLimit).
		Find(&history).Error; err != nil {
		return nil, nil, errors.Wrap(err, "query history requests")
	}

	return pending, history, nil
}

// AnswerRequest stores the human response and marks the request as answered.
func (s *Service) AnswerRequest(ctx context.Context, auth *AuthorizationContext, id uuid.UUID, answer string) (*Request, error) {
	if auth == nil {
		return nil, ErrInvalidAuthorization
	}

	var req Request
	if err := s.db.WithContext(ctx).
		Where("id = ? AND api_key_hash = ?", id, auth.APIKeyHash).
		First(&req).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRequestNotFound
		}
		return nil, errors.Wrap(err, "lookup ask_user request")
	}

	if req.Status != StatusPending {
		return &req, nil
	}

	now := time.Now().UTC()
	req.Answer = &answer
	req.Status = StatusAnswered
	req.AnsweredAt = &now

	if err := s.db.WithContext(ctx).Model(&req).Updates(map[string]any{
		"answer":      answer,
		"status":      StatusAnswered,
		"answered_at": now,
		"updated_at":  now,
	}).Error; err != nil {
		return nil, errors.Wrap(err, "update ask_user request with answer")
	}

	s.log().Info("ask_user request answered",
		zap.String("request_id", req.ID.String()),
		zap.String("user", auth.UserIdentity),
	)

	return &req, nil
}

// getByID retrieves a request by ID within the provided context.
func (s *Service) getByID(ctx context.Context, id uuid.UUID) (*Request, error) {
	var req Request
	if err := s.db.WithContext(ctx).First(&req, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRequestNotFound
		}
		return nil, errors.Wrap(err, "query ask_user request")
	}
	return &req, nil
}

func (s *Service) log() logSDK.Logger {
	return s.logger
}
