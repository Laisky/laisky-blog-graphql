package userrequests

import (
	"context"
	"strings"

	errors "github.com/Laisky/errors/v2"
	"github.com/Laisky/zap"
	"github.com/google/uuid"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
)

// CreateSavedCommand stores a new saved command for the authenticated user.
func (s *Service) CreateSavedCommand(ctx context.Context, auth *askuser.AuthorizationContext, label, content string) (*SavedCommand, error) {
	if auth == nil {
		return nil, ErrInvalidAuthorization
	}

	label = sanitizeSavedCommandLabel(label)
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, ErrEmptyContent
	}
	if len(content) > MaxSavedCommandContentLength {
		content = content[:MaxSavedCommandContentLength]
	}

	// Check user's existing command count
	var count int64
	if err := s.db.WithContext(ctx).
		Model(&SavedCommand{}).
		Where("api_key_hash = ?", auth.APIKeyHash).
		Count(&count).Error; err != nil {
		return nil, errors.Wrap(err, "count saved commands")
	}
	if count >= MaxSavedCommandsPerUser {
		return nil, ErrSavedCommandLimitReached
	}

	// Get the next sort order
	var maxOrder int
	s.db.WithContext(ctx).
		Model(&SavedCommand{}).
		Where("api_key_hash = ?", auth.APIKeyHash).
		Select("COALESCE(MAX(sort_order), -1)").
		Row().
		Scan(&maxOrder)

	cmd := &SavedCommand{
		Label:        label,
		Content:      content,
		SortOrder:    maxOrder + 1,
		APIKeyHash:   auth.APIKeyHash,
		KeySuffix:    auth.KeySuffix,
		UserIdentity: auth.UserIdentity,
	}

	if err := s.db.WithContext(ctx).Create(cmd).Error; err != nil {
		return nil, errors.Wrap(err, "create saved command")
	}

	s.log().Info("saved command created",
		zap.String("command_id", cmd.ID.String()),
		zap.String("user", auth.UserIdentity),
		zap.String("label", cmd.Label),
	)

	return cmd, nil
}

// ListSavedCommands returns all saved commands for the authenticated user, ordered by sort_order.
func (s *Service) ListSavedCommands(ctx context.Context, auth *askuser.AuthorizationContext) ([]SavedCommand, error) {
	if auth == nil {
		return nil, ErrInvalidAuthorization
	}

	commands := make([]SavedCommand, 0)
	if err := s.db.WithContext(ctx).
		Where("api_key_hash = ?", auth.APIKeyHash).
		Order("sort_order ASC, created_at ASC").
		Limit(MaxSavedCommandsPerUser).
		Find(&commands).Error; err != nil {
		return nil, errors.Wrap(err, "list saved commands")
	}

	return commands, nil
}

// UpdateSavedCommand modifies an existing saved command belonging to the authenticated user.
func (s *Service) UpdateSavedCommand(ctx context.Context, auth *askuser.AuthorizationContext, id uuid.UUID, label, content *string, sortOrder *int) (*SavedCommand, error) {
	if auth == nil {
		return nil, ErrInvalidAuthorization
	}

	var cmd SavedCommand
	if err := s.db.WithContext(ctx).
		Where("id = ? AND api_key_hash = ?", id, auth.APIKeyHash).
		First(&cmd).Error; err != nil {
		return nil, ErrSavedCommandNotFound
	}

	updates := make(map[string]any)
	if label != nil {
		updates["label"] = sanitizeSavedCommandLabel(*label)
	}
	if content != nil {
		c := strings.TrimSpace(*content)
		if c == "" {
			return nil, ErrEmptyContent
		}
		if len(c) > MaxSavedCommandContentLength {
			c = c[:MaxSavedCommandContentLength]
		}
		updates["content"] = c
	}
	if sortOrder != nil {
		updates["sort_order"] = *sortOrder
	}

	if len(updates) == 0 {
		return &cmd, nil
	}

	updates["updated_at"] = s.clock()

	if err := s.db.WithContext(ctx).
		Model(&cmd).
		Updates(updates).Error; err != nil {
		return nil, errors.Wrap(err, "update saved command")
	}

	// Refresh from DB
	if err := s.db.WithContext(ctx).First(&cmd, "id = ?", id).Error; err != nil {
		return nil, errors.Wrap(err, "reload saved command after update")
	}

	s.log().Info("saved command updated",
		zap.String("command_id", cmd.ID.String()),
		zap.String("user", auth.UserIdentity),
	)

	return &cmd, nil
}

// DeleteSavedCommand removes a single saved command belonging to the authenticated user.
func (s *Service) DeleteSavedCommand(ctx context.Context, auth *askuser.AuthorizationContext, id uuid.UUID) error {
	if auth == nil {
		return ErrInvalidAuthorization
	}

	result := s.db.WithContext(ctx).
		Where("id = ? AND api_key_hash = ?", id, auth.APIKeyHash).
		Delete(&SavedCommand{})
	if result.Error != nil {
		return errors.Wrap(result.Error, "delete saved command")
	}
	if result.RowsAffected == 0 {
		return ErrSavedCommandNotFound
	}

	s.log().Info("saved command deleted",
		zap.String("command_id", id.String()),
		zap.String("user", auth.UserIdentity),
	)

	return nil
}

// ReorderSavedCommands updates the sort order for multiple saved commands at once.
func (s *Service) ReorderSavedCommands(ctx context.Context, auth *askuser.AuthorizationContext, orderedIDs []uuid.UUID) error {
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
		result := tx.Model(&SavedCommand{}).
			Where("id = ? AND api_key_hash = ?", id, auth.APIKeyHash).
			Update("sort_order", i)
		if result.Error != nil {
			tx.Rollback()
			return errors.Wrap(result.Error, "update sort order")
		}
	}

	if err := tx.Commit().Error; err != nil {
		return errors.Wrap(err, "commit reorder transaction")
	}

	s.log().Info("saved commands reordered",
		zap.Int("count", len(orderedIDs)),
		zap.String("user", auth.UserIdentity),
	)

	return nil
}

func sanitizeSavedCommandLabel(input string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "Untitled Command"
	}
	if len(trimmed) > MaxSavedCommandLabelLength {
		return trimmed[:MaxSavedCommandLabelLength]
	}
	return trimmed
}
