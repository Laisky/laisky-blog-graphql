package userrequests

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// useDollarPlaceholders reports whether the SQL dialect expects $1-style bind placeholders.
func useDollarPlaceholders(db *sql.DB) bool {
	if db == nil {
		return false
	}

	driverName := strings.ToLower(fmt.Sprintf("%T", db.Driver()))
	return !strings.Contains(driverName, "sqlite")
}

// rebindQuery rewrites question-mark placeholders into dialect-specific placeholders.
func rebindQuery(query string, dollar bool) string {
	if !dollar {
		return query
	}

	var builder strings.Builder
	builder.Grow(len(query) + 8)
	argIdx := 1
	for _, ch := range query {
		if ch == '?' {
			builder.WriteString(fmt.Sprintf("$%d", argIdx))
			argIdx++
			continue
		}
		builder.WriteRune(ch)
	}

	return builder.String()
}

// execContext executes an SQL statement after rebinding placeholders for the active dialect.
func (s *Service) execContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, rebindQuery(query, s.useDollar), args...)
}

// queryContext executes a query after rebinding placeholders for the active dialect.
func (s *Service) queryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, rebindQuery(query, s.useDollar), args...)
}

// queryRowContext executes a row query after rebinding placeholders for the active dialect.
func (s *Service) queryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, rebindQuery(query, s.useDollar), args...)
}
