package rag

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

func TestEnsureVectorExtensionPostgresSuccess(t *testing.T) {
	t.Parallel()

	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer sqlDB.Close()

	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sqlDB,
	}))
	require.NoError(t, err)

	mock.ExpectExec(`CREATE EXTENSION IF NOT EXISTS vector`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = ensureVectorExtension(context.Background(), gdb, log.Logger.Named("test"))
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEnsureVectorExtensionFallback(t *testing.T) {
	t.Parallel()

	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer sqlDB.Close()

	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sqlDB,
	}))
	require.NoError(t, err)

	pgErr := &pgconn.PgError{Code: "58P01", Message: "extension \"vector\" is not available"}

	mock.ExpectExec(`CREATE EXTENSION IF NOT EXISTS vector`).
		WillReturnError(pgErr)
	mock.ExpectExec(`CREATE EXTENSION IF NOT EXISTS pgvector`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = ensureVectorExtension(context.Background(), gdb, log.Logger.Named("test"))
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEnsureVectorExtensionSkipNonPostgres(t *testing.T) {
	t.Parallel()

	gdb, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)

	err = ensureVectorExtension(context.Background(), gdb, log.Logger.Named("test"))
	require.NoError(t, err)
}
