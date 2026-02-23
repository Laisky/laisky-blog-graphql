package files

import (
	"context"
	"database/sql"
	"strings"

	errors "github.com/Laisky/errors/v2"
)

// sqlDBTX describes operations shared by sql.DB and sql.Tx.
type sqlDBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// detectPostgresDialect reports whether the current database is PostgreSQL.
func detectPostgresDialect(ctx context.Context, db *sql.DB) (bool, error) {
	if db == nil {
		return false, errors.New("sql db is required")
	}

	const query = "SELECT current_setting('server_version_num')"
	var version string
	if err := db.QueryRowContext(ctx, query).Scan(&version); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "current_setting") {
			return false, nil
		}
		if strings.Contains(strings.ToLower(err.Error()), "no such function") {
			return false, nil
		}
		if strings.Contains(strings.ToLower(err.Error()), "syntax error") {
			return false, nil
		}
		return false, errors.Wrap(err, "probe postgres current_setting")
	}

	return strings.TrimSpace(version) != "", nil
}

// rebindSQL rewrites positional placeholders for PostgreSQL.
func rebindSQL(query string, isPostgres bool) string {
	if !isPostgres {
		return query
	}

	var builder strings.Builder
	builder.Grow(len(query) + 8)
	argIndex := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			builder.WriteString("$")
			builder.WriteString(strconvItoa(argIndex))
			argIndex++
			continue
		}
		builder.WriteByte(query[i])
	}

	return builder.String()
}

// strconvItoa converts a non-negative integer to decimal text.
func strconvItoa(value int) string {
	if value == 0 {
		return "0"
	}

	buf := [20]byte{}
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + (value % 10))
		value /= 10
	}
	return string(buf[i:])
}
