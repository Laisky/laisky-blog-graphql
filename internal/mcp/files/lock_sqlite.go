package files

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strconv"
	"strings"

	errors "github.com/Laisky/errors/v2"
)

// withSQLiteWriteIntent acquires SQLite's database write lock before any
// mutation reads. FileIO accepts database/sql pools whose BeginTx can be
// deferred or immediate, so explicitly promote a deferred transaction without
// changing any rows or adding a lock table.
func withSQLiteWriteIntent(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return errors.Wrap(err, "reserve SQLite mutation connection")
	}
	defer func() { _ = conn.Close() }()
	var timeout int
	if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
		return errors.Wrap(err, "read SQLite mutation busy timeout")
	}
	// SQLite PRAGMA assignment does not support bind parameters.
	restore := "PRAGMA busy_timeout=" + strconv.Itoa(timeout) //nolint:gosec // timeout is an integer read from SQLite, not user input
	restored := false
	defer func() {
		if !restored {
			if _, err := conn.ExecContext(context.WithoutCancel(ctx), restore); err != nil {
				// Do not return a connection with altered session settings to the pool.
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
	}()

	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout=0"); err != nil {
		return errors.Wrap(err, "set SQLite nonblocking admission")
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return sqliteWriteAdmissionError(err)
	}
	defer func() { _ = tx.Rollback() }()
	// system_owner-checked: this zero-row statement acquires write intent without touching any tenant or system row.
	if _, err := tx.ExecContext(ctx, `UPDATE mcp_files SET id = id WHERE 0`); err != nil {
		return sqliteWriteAdmissionError(err)
	}
	if _, err := tx.ExecContext(ctx, restore); err != nil {
		return errors.Wrap(err, "restore SQLite busy timeout")
	}
	restored = true
	return finishMutation(tx, fn)
}

// sqliteWriteAdmissionError only classifies errors from SQLite's BEGIN or the
// write-intent statement, never from the mutation callback or COMMIT.
func sqliteWriteAdmissionError(err error) error {
	// SQLite's canonical SQLITE_BUSY/SQLITE_LOCKED messages, as returned by
	// the repository's database/sql driver. Keep production files CGO-independent.
	msg := err.Error()
	if msg == "database is locked" || msg == "database table is locked" || strings.HasPrefix(msg, "database table is locked: ") ||
		msg == "database schema is locked" || strings.HasPrefix(msg, "database schema is locked: ") {
		return NewError(ErrCodeResourceBusy, "resource busy", true)
	}
	return errors.Wrap(err, "acquire SQLite write intent")
}
