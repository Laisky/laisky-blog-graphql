package files

import (
	"context"
	"database/sql"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// TestDefaultLockProviderRetriesUntilLockAcquired verifies advisory lock retries succeed before timeout.
func TestDefaultLockProviderRetriesUntilLockAcquired(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	provider := DefaultLockProvider{}
	lockKey := hashLockKey("tenant-a", "proj")
	callbackCalls := 0

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT pg_try_advisory_xact_lock\(\$1\)`).
		WithArgs(lockKey).
		WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_xact_lock"}).AddRow(false))
	mock.ExpectQuery(`SELECT pg_try_advisory_xact_lock\(\$1\)`).
		WithArgs(lockKey).
		WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_xact_lock"}).AddRow(true))
	mock.ExpectCommit()

	err = provider.WithProjectLock(context.Background(), sqlDB, true, "tenant-a", "proj", 150*time.Millisecond, func(_ *sql.Tx) error {
		callbackCalls++
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, callbackCalls)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDefaultLockProviderReturnsResourceBusyOnTimeout verifies lock contention returns RESOURCE_BUSY after timeout.
func TestDefaultLockProviderReturnsResourceBusyOnTimeout(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	provider := DefaultLockProvider{}
	lockKey := hashLockKey("tenant-a", "proj")

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT pg_try_advisory_xact_lock\(\$1\)`).
		WithArgs(lockKey).
		WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_xact_lock"}).AddRow(false))
	mock.ExpectQuery(`SELECT pg_try_advisory_xact_lock\(\$1\)`).
		WithArgs(lockKey).
		WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_xact_lock"}).AddRow(false))
	mock.ExpectRollback()

	err = provider.WithProjectLock(context.Background(), sqlDB, true, "tenant-a", "proj", 10*time.Millisecond, func(_ *sql.Tx) error {
		return nil
	})
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodeResourceBusy))
	require.NoError(t, mock.ExpectationsWereMet())
}
