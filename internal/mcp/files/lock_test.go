package files

import (
	"context"
	"database/sql"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	errors "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

func TestTenantLockAttemptsOnceAndReturnsBusy(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT pg_try_advisory_xact_lock\(\$1\)`).WithArgs(tenantLockKey("a")).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(false))
	mock.ExpectRollback()
	calls := 0
	err = (DefaultLockProvider{}).WithTenantLock(context.Background(), db, true, "a", func(*sql.Tx) error {
		calls++
		return nil
	})
	require.True(t, IsCode(err, ErrCodeResourceBusy), "%v", err)
	typed, ok := AsError(err)
	require.True(t, ok)
	require.True(t, typed.Retryable)
	require.Zero(t, calls)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantLockDoesNotClassifyCommitFailureAsBusy(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT pg_try_advisory_xact_lock\(\$1\)`).WithArgs(tenantLockKey("a")).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectCommit().WillReturnError(errors.New("connection lost during commit"))
	calls := 0
	err = (DefaultLockProvider{}).WithTenantLock(context.Background(), db, true, "a", func(*sql.Tx) error {
		calls++
		return nil
	})
	require.ErrorContains(t, err, "commit mutation transaction")
	require.False(t, IsCode(err, ErrCodeResourceBusy))
	require.Equal(t, 1, calls)
	require.NoError(t, mock.ExpectationsWereMet())
}
