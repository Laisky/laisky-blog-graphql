package userrequests

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
)

func TestServiceLifecycle(t *testing.T) {
	db := newTestDB(t)
	clock := fixedClock(time.Date(2024, 10, 1, 12, 0, 0, 0, time.UTC))
	svc, err := NewService(db, nil, clock.Now)
	require.NoError(t, err)

	auth := testAuth("hash-1", "abcd")
	ctx := context.Background()

	created, err := svc.CreateRequest(ctx, auth, "First directive", "")
	require.NoError(t, err)
	require.Equal(t, StatusPending, created.Status)
	require.Equal(t, DefaultTaskID, created.TaskID)

	pending, consumed, err := svc.ListRequests(ctx, auth)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Len(t, consumed, 0)

	second, err := svc.CreateRequest(ctx, auth, "Second directive", "alpha")
	require.NoError(t, err)
	require.Equal(t, "alpha", second.TaskID)

	_, _, err = svc.ListRequests(ctx, auth)
	require.NoError(t, err)

	consumedReq, err := svc.ConsumeLatestPending(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, second.ID, consumedReq.ID, "latest request should be consumed first")
	require.Equal(t, StatusConsumed, consumedReq.Status)
	require.NotNil(t, consumedReq.ConsumedAt)

	pending, consumed, err = svc.ListRequests(ctx, auth)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, created.ID, pending[0].ID)
	require.Len(t, consumed, 1)
	require.Equal(t, consumedReq.ID, consumed[0].ID)

	// consume remaining request
	_, err = svc.ConsumeLatestPending(ctx, auth)
	require.NoError(t, err)

	_, err = svc.ConsumeLatestPending(ctx, auth)
	require.ErrorIs(t, err, ErrNoPendingRequests)
}

func TestServiceDeleteOperations(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() })
	require.NoError(t, err)

	authA := testAuth("hash-a", "aaaa")
	authB := testAuth("hash-b", "bbbb")

	ctx := context.Background()

	reqA, err := svc.CreateRequest(ctx, authA, "Directive A", "")
	require.NoError(t, err)
	_, err = svc.CreateRequest(ctx, authB, "Directive B", "")
	require.NoError(t, err)

	require.NoError(t, svc.DeleteRequest(ctx, authA, reqA.ID))
	require.ErrorIs(t, svc.DeleteRequest(ctx, authA, reqA.ID), ErrRequestNotFound)

	deleted, err := svc.DeleteAll(ctx, authB)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
}

type testClock struct {
	now time.Time
}

func (c *testClock) Now() time.Time {
	return c.now
}

func fixedClock(ts time.Time) *testClock {
	return &testClock{now: ts}
}

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:userrequests_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	return db
}

func testAuth(hash, suffix string) *askuser.AuthorizationContext {
	return &askuser.AuthorizationContext{
		APIKeyHash:   hash,
		KeySuffix:    suffix,
		UserIdentity: "user-" + hash,
	}
}

func TestSanitizeTaskID(t *testing.T) {
	require.Equal(t, DefaultTaskID, sanitizeTaskID(""))
	require.Equal(t, "abc", sanitizeTaskID(" abc "))

	long := strings.Repeat("x", maxTaskIDLength+10)
	trimmed := sanitizeTaskID(long)
	require.Equal(t, maxTaskIDLength, len(trimmed))
}
