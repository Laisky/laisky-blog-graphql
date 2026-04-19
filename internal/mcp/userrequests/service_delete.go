package userrequests

import (
	"context"

	errors "github.com/Laisky/errors/v2"
	"github.com/Laisky/zap"
	"github.com/google/uuid"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
)

// DeleteRequest removes a single request belonging to the authenticated user.
func (s *Service) DeleteRequest(ctx context.Context, auth *askuser.AuthorizationContext, id uuid.UUID, taskID string) error {
	if auth == nil {
		return ErrInvalidAuthorization
	}

	result, err := s.execContext(ctx,
		`DELETE FROM mcp_user_requests WHERE id = ? AND api_key_hash = ?`,
		id.String(),
		auth.APIKeyHash,
	)
	if err != nil {
		return errors.Wrap(err, "delete user request")
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return errors.Wrap(err, "read deleted rows affected")
	}
	if rowsAffected == 0 {
		return ErrRequestNotFound
	}
	s.log().Debug("deleted user request",
		zap.String("user", auth.UserIdentity),
		zap.String("request_id", id.String()),
		zap.Int64("deleted", rowsAffected),
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
	query := `DELETE FROM mcp_user_requests WHERE api_key_hash = ?`
	args := []any{auth.APIKeyHash}
	if !includeAllTasks {
		query += sqlAndTaskID
		args = append(args, filteredTaskID)
	}

	result, err := s.execContext(ctx, query, args...)
	if err != nil {
		return 0, errors.Wrap(err, "delete all user requests")
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, errors.Wrap(err, "read deleted rows affected")
	}
	logTaskID := filteredTaskID
	if includeAllTasks {
		logTaskID = "*"
	}
	s.log().Debug("deleted user requests",
		zap.String("user", auth.UserIdentity),
		zap.Bool("all_tasks", includeAllTasks),
		zap.String("task_id", logTaskID),
		zap.Int64("deleted", rowsAffected),
	)
	return rowsAffected, nil
}

// DeleteAllPending removes pending requests. When includeAllTasks is false the operation is
// restricted to the provided taskID.
func (s *Service) DeleteAllPending(ctx context.Context, auth *askuser.AuthorizationContext, taskID string, includeAllTasks bool) (int64, error) {
	if auth == nil {
		return 0, ErrInvalidAuthorization
	}

	filteredTaskID := normalizeTaskID(taskID)
	query := `DELETE FROM mcp_user_requests WHERE api_key_hash = ? AND status = ?`
	args := []any{auth.APIKeyHash, StatusPending}
	if !includeAllTasks {
		query += sqlAndTaskID
		args = append(args, filteredTaskID)
	}

	result, err := s.execContext(ctx, query, args...)
	if err != nil {
		return 0, errors.Wrap(err, "delete pending user requests")
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, errors.Wrap(err, "read deleted rows affected")
	}
	logTaskID := filteredTaskID
	if includeAllTasks {
		logTaskID = "*"
	}
	s.log().Debug("deleted pending user requests",
		zap.String("user", auth.UserIdentity),
		zap.Bool("all_tasks", includeAllTasks),
		zap.String("task_id", logTaskID),
		zap.Int64("deleted", rowsAffected),
	)
	return rowsAffected, nil
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
	query := `DELETE FROM mcp_user_requests WHERE api_key_hash = ? AND status = ?`
	args := []any{auth.APIKeyHash, StatusConsumed}
	if !includeAllTasks {
		query += sqlAndTaskID
		args = append(args, filteredTaskID)
	}

	if keepCount > 0 {
		subQuery := `SELECT id FROM mcp_user_requests WHERE api_key_hash = ? AND status = ?`
		subArgs := []any{auth.APIKeyHash, StatusConsumed}
		if !includeAllTasks {
			subQuery += sqlAndTaskID
			subArgs = append(subArgs, filteredTaskID)
		}
		subQuery += ` ORDER BY consumed_at DESC LIMIT ?`
		subArgs = append(subArgs, keepCount)

		query += ` AND id NOT IN (` + subQuery + `)`
		args = append(args, subArgs...)
	} else if keepDays > 0 {
		cutoff := s.clock().AddDate(0, 0, -keepDays)
		query += ` AND consumed_at < ?`
		args = append(args, cutoff)
	}

	result, err := s.execContext(ctx, query, args...)
	if err != nil {
		return 0, errors.Wrap(err, "delete consumed requests")
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, errors.Wrap(err, "read deleted rows affected")
	}
	logTaskID := filteredTaskID
	if includeAllTasks {
		logTaskID = "*"
	}
	s.log().Debug("deleted consumed user requests",
		zap.String("user", auth.UserIdentity),
		zap.Bool("all_tasks", includeAllTasks),
		zap.String("task_id", logTaskID),
		zap.Int64("deleted", rowsAffected),
	)
	return rowsAffected, nil
}

// pruneExpired deletes requests older than the configured retention window.
func (s *Service) pruneExpired(ctx context.Context) error {
	if s == nil || s.settings.RetentionDays <= 0 {
		return nil
	}
	cutoff := s.clock().AddDate(0, 0, -s.settings.RetentionDays)
	_, err := s.execContext(ctx,
		`DELETE FROM mcp_user_requests WHERE created_at < ?`,
		cutoff,
	)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			s.log().Debug("prune expired aborted", zap.Error(err))
			return nil
		default:
			return errors.Wrap(err, "prune expired user requests")
		}
	}
	return nil
}
