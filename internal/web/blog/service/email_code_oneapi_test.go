package service

import (
	"context"
	"testing"

	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	blogoneapi "github.com/Laisky/laisky-blog-graphql/internal/web/blog/oneapi"
)

// TestOneAPIEmailCodeStorageFailureIsNotABadCode proves a OneAPI database
// failure while checking an email code is reported as a storage failure, not
// as a wrong code, so it never counts toward the client's risk failures.
func TestOneAPIEmailCodeStorageFailureIsNotABadCode(t *testing.T) {
	db, err := blogoneapi.NewDB(t.Context(), blogoneapi.Options{Driver: "sqlite", SQLitePath: "file:email_code_storage_failure?mode=memory&cache=shared"})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	svc := &Blog{logger: glog.Shared, oneapi: blogoneapi.New(glog.Shared, db)}
	_, err = svc.ValidateEmailCodeLogin(context.Background(), "owner@example.test", "123456")
	require.Error(t, err)
	require.NotErrorIs(t, err, model.ErrInvalidCredentials)
	require.NotErrorIs(t, err, errInvalidEmailVerificationCode)
}
