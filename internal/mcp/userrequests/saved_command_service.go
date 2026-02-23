package userrequests

import (
	"context"
	"database/sql"
	"strings"

	errors "github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
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

	var count int64
	if err := s.queryRowContext(ctx,
		`SELECT COUNT(1) FROM mcp_saved_commands WHERE api_key_hash = ?`,
		auth.APIKeyHash,
	).Scan(&count); err != nil {
		return nil, errors.Wrap(err, "count saved commands")
	}
	if count >= MaxSavedCommandsPerUser {
		return nil, ErrSavedCommandLimitReached
	}

	var maxOrder int
	if err := s.queryRowContext(ctx,
		`SELECT COALESCE(MAX(sort_order), -1) FROM mcp_saved_commands WHERE api_key_hash = ?`,
		auth.APIKeyHash,
	).Scan(&maxOrder); err != nil {
		return nil, errors.Wrap(err, "query saved command max sort order")
	}

	now := s.clock()

	cmd := &SavedCommand{
		ID:           gutils.UUID7Bytes(),
		Label:        label,
		Content:      content,
		SortOrder:    maxOrder + 1,
		APIKeyHash:   auth.APIKeyHash,
		KeySuffix:    auth.KeySuffix,
		UserIdentity: auth.UserIdentity,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if _, err := s.execContext(ctx,
		`INSERT INTO mcp_saved_commands
		 (id, label, content, sort_order, api_key_hash, key_suffix, user_identity, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cmd.ID.String(),
		cmd.Label,
		cmd.Content,
		cmd.SortOrder,
		cmd.APIKeyHash,
		cmd.KeySuffix,
		cmd.UserIdentity,
		cmd.CreatedAt,
		cmd.UpdatedAt,
	); err != nil {
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

	rows, err := s.queryContext(ctx,
		`SELECT id, label, content, sort_order, api_key_hash, key_suffix, user_identity, created_at, updated_at
		 FROM mcp_saved_commands
		 WHERE api_key_hash = ?
		 ORDER BY sort_order ASC, created_at ASC
		 LIMIT ?`,
		auth.APIKeyHash,
		MaxSavedCommandsPerUser,
	)
	if err != nil {
		return nil, errors.Wrap(err, "list saved commands")
	}

	commands, err := scanSavedCommandRows(rows)
	if err != nil {
		return nil, errors.Wrap(err, "scan saved commands")
	}

	return commands, nil
}

// UpdateSavedCommand modifies an existing saved command belonging to the authenticated user.
func (s *Service) UpdateSavedCommand(ctx context.Context, auth *askuser.AuthorizationContext, id uuid.UUID, label, content *string, sortOrder *int) (*SavedCommand, error) {
	if auth == nil {
		return nil, ErrInvalidAuthorization
	}

	cmd, err := scanSavedCommandRow(s.queryRowContext(ctx,
		`SELECT id, label, content, sort_order, api_key_hash, key_suffix, user_identity, created_at, updated_at
		 FROM mcp_saved_commands
		 WHERE id = ? AND api_key_hash = ?
		 LIMIT 1`,
		id.String(),
		auth.APIKeyHash,
	))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSavedCommandNotFound
		}
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
		return cmd, nil
	}

	updates["updated_at"] = s.clock()

	assignments := make([]string, 0, len(updates))
	args := make([]any, 0, len(updates)+2)
	for column, value := range updates {
		assignments = append(assignments, column+" = ?")
		args = append(args, value)
	}
	args = append(args, id.String(), auth.APIKeyHash)

	query := `UPDATE mcp_saved_commands SET ` + strings.Join(assignments, ", ") + ` WHERE id = ? AND api_key_hash = ?`
	if _, err := s.execContext(ctx, query, args...); err != nil {
		return nil, errors.Wrap(err, "update saved command")
	}

	cmd, err = scanSavedCommandRow(s.queryRowContext(ctx,
		`SELECT id, label, content, sort_order, api_key_hash, key_suffix, user_identity, created_at, updated_at
		 FROM mcp_saved_commands
		 WHERE id = ?
		 LIMIT 1`,
		id.String(),
	))
	if err != nil {
		return nil, errors.Wrap(err, "reload saved command after update")
	}

	s.log().Info("saved command updated",
		zap.String("command_id", cmd.ID.String()),
		zap.String("user", auth.UserIdentity),
	)

	return cmd, nil
}

// DeleteSavedCommand removes a single saved command belonging to the authenticated user.
func (s *Service) DeleteSavedCommand(ctx context.Context, auth *askuser.AuthorizationContext, id uuid.UUID) error {
	if auth == nil {
		return ErrInvalidAuthorization
	}

	result, err := s.execContext(ctx,
		`DELETE FROM mcp_saved_commands WHERE id = ? AND api_key_hash = ?`,
		id.String(),
		auth.APIKeyHash,
	)
	if err != nil {
		return errors.Wrap(err, "delete saved command")
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return errors.Wrap(err, "read deleted saved command rows affected")
	}
	if rowsAffected == 0 {
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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Wrap(err, "begin reorder transaction")
	}
	defer func() {
		if r := recover(); r != nil {
			_ = tx.Rollback()
		}
	}()

	for i, id := range orderedIDs {
		_, execErr := tx.ExecContext(ctx,
			rebindQuery(`UPDATE mcp_saved_commands SET sort_order = ?, updated_at = ? WHERE id = ? AND api_key_hash = ?`, s.useDollar),
			i,
			s.clock(),
			id.String(),
			auth.APIKeyHash,
		)
		if execErr != nil {
			_ = tx.Rollback()
			return errors.Wrap(execErr, "update sort order")
		}
	}

	if err := tx.Commit(); err != nil {
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

// scanSavedCommandRows reads saved command rows into models.
func scanSavedCommandRows(rows *sql.Rows) ([]SavedCommand, error) {
	defer rows.Close()

	items := make([]SavedCommand, 0)
	for rows.Next() {
		item, err := scanSavedCommandValues(rows.Scan)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		items = append(items, *item)
	}

	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate saved command rows")
	}

	return items, nil
}

// scanSavedCommandRow reads one saved command row into a model.
func scanSavedCommandRow(row *sql.Row) (*SavedCommand, error) {
	item, err := scanSavedCommandValues(row.Scan)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return item, nil
}

// scanSavedCommandValues extracts a SavedCommand from a scanner callback.
func scanSavedCommandValues(scanFn func(dest ...any) error) (*SavedCommand, error) {
	var (
		idRaw        string
		createdAtRaw any
		updatedAtRaw any
		item         SavedCommand
	)
	if err := scanFn(
		&idRaw,
		&item.Label,
		&item.Content,
		&item.SortOrder,
		&item.APIKeyHash,
		&item.KeySuffix,
		&item.UserIdentity,
		&createdAtRaw,
		&updatedAtRaw,
	); err != nil {
		return nil, errors.Wrap(err, "scan saved command row")
	}

	parsedID, err := uuid.Parse(idRaw)
	if err != nil {
		return nil, errors.Wrap(err, "parse saved command id")
	}
	item.ID = parsedID

	item.CreatedAt, err = parseSQLTime(createdAtRaw)
	if err != nil {
		return nil, errors.Wrap(err, "parse saved command created_at")
	}
	item.UpdatedAt, err = parseSQLTime(updatedAtRaw)
	if err != nil {
		return nil, errors.Wrap(err, "parse saved command updated_at")
	}

	return &item, nil
}
