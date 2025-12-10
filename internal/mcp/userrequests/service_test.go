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
	svc, err := NewService(db, nil, clock.Now, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	auth := testAuth("hash-1", "abcd")
	ctx := context.Background()

	created, err := svc.CreateRequest(ctx, auth, "First directive", "")
	require.NoError(t, err)
	require.Equal(t, StatusPending, created.Status)
	require.Equal(t, DefaultTaskID, created.TaskID)

	pending, consumed, err := svc.ListRequests(ctx, auth, "")
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Len(t, consumed, 0)

	second, err := svc.CreateRequest(ctx, auth, "Second directive", "")
	require.NoError(t, err)
	require.Equal(t, DefaultTaskID, second.TaskID)

	_, _, err = svc.ListRequests(ctx, auth, "")
	require.NoError(t, err)

	// ConsumeAllPending should return all pending in FIFO order (oldest first)
	consumedReqs, err := svc.ConsumeAllPending(ctx, auth, "")
	require.NoError(t, err)
	require.Len(t, consumedReqs, 2)
	require.Equal(t, created.ID, consumedReqs[0].ID, "first created should be first (FIFO)")
	require.Equal(t, second.ID, consumedReqs[1].ID, "second created should be second (FIFO)")
	require.Equal(t, StatusConsumed, consumedReqs[0].Status)
	require.Equal(t, StatusConsumed, consumedReqs[1].Status)
	require.NotNil(t, consumedReqs[0].ConsumedAt)
	require.NotNil(t, consumedReqs[1].ConsumedAt)

	pending, consumed, err = svc.ListRequests(ctx, auth, "")
	require.NoError(t, err)
	require.Len(t, pending, 0)
	require.Len(t, consumed, 2)

	// No more pending requests
	_, err = svc.ConsumeAllPending(ctx, auth, "")
	require.ErrorIs(t, err, ErrNoPendingRequests)
}

func TestServiceDeleteOperations(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	authA := testAuth("hash-a", "aaaa")
	authB := testAuth("hash-b", "bbbb")

	ctx := context.Background()

	reqA, err := svc.CreateRequest(ctx, authA, "Directive A", "")
	require.NoError(t, err)
	_, err = svc.CreateRequest(ctx, authB, "Directive B", "")
	require.NoError(t, err)

	require.NoError(t, svc.DeleteRequest(ctx, authA, reqA.ID, ""))
	require.ErrorIs(t, svc.DeleteRequest(ctx, authA, reqA.ID, ""), ErrRequestNotFound)

	deleted, err := svc.DeleteAll(ctx, authB, "")
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
}

func TestServicePrunesExpiredRequests(t *testing.T) {
	db := newTestDB(t)
	clock := fixedClock(time.Date(2024, 11, 1, 12, 0, 0, 0, time.UTC))
	settings := Settings{RetentionDays: 30}
	svc, err := NewService(db, nil, clock.Now, settings)
	require.NoError(t, err)

	auth := testAuth("hash-prune", "abcd")
	ctx := context.Background()

	oldReq, err := svc.CreateRequest(ctx, auth, "Expired directive", "task-expired")
	require.NoError(t, err)
	recentReq, err := svc.CreateRequest(ctx, auth, "Fresh directive", "task-expired")
	require.NoError(t, err)

	oldCreatedAt := clock.Now().AddDate(0, 0, -(settings.RetentionDays + 5))
	require.NoError(t, db.Model(&Request{}).Where("id = ?", oldReq.ID).Update("created_at", oldCreatedAt).Error)

	pending, _, err := svc.ListRequests(ctx, auth, "task-expired")
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, recentReq.ID, pending[0].ID)
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

func TestNormalizeTaskID(t *testing.T) {
	require.Equal(t, DefaultTaskID, normalizeTaskID(""))
	require.Equal(t, "abc", normalizeTaskID(" abc "))

	long := strings.Repeat("x", maxTaskIDLength+10)
	trimmed := normalizeTaskID(long)
	require.Equal(t, 64, len(trimmed))
}

// TestServiceConsumeFirstPending verifies that ConsumeFirstPending returns
// only the oldest pending request (FIFO order) and marks it as consumed.
func TestServiceConsumeFirstPending(t *testing.T) {
	db := newTestDB(t)
	clock := fixedClock(time.Date(2024, 10, 1, 12, 0, 0, 0, time.UTC))
	svc, err := NewService(db, nil, clock.Now, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	auth := testAuth("hash-first", "abcd")
	ctx := context.Background()

	// Create three requests
	first, err := svc.CreateRequest(ctx, auth, "First directive", "")
	require.NoError(t, err)

	second, err := svc.CreateRequest(ctx, auth, "Second directive", "")
	require.NoError(t, err)

	third, err := svc.CreateRequest(ctx, auth, "Third directive", "")
	require.NoError(t, err)

	// ConsumeFirstPending should return only the first (oldest) request
	consumed, err := svc.ConsumeFirstPending(ctx, auth, "")
	require.NoError(t, err)
	require.NotNil(t, consumed)
	require.Equal(t, first.ID, consumed.ID, "should return the oldest request")
	require.Equal(t, "First directive", consumed.Content)
	require.Equal(t, StatusConsumed, consumed.Status)
	require.NotNil(t, consumed.ConsumedAt)

	// Verify second and third are still pending
	pending, _, err := svc.ListRequests(ctx, auth, "")
	require.NoError(t, err)
	require.Len(t, pending, 2)
	require.Equal(t, second.ID, pending[0].ID, "second should be first in pending now")
	require.Equal(t, third.ID, pending[1].ID, "third should be second in pending")

	// Consume second
	consumed2, err := svc.ConsumeFirstPending(ctx, auth, "")
	require.NoError(t, err)
	require.Equal(t, second.ID, consumed2.ID)

	// Consume third
	consumed3, err := svc.ConsumeFirstPending(ctx, auth, "")
	require.NoError(t, err)
	require.Equal(t, third.ID, consumed3.ID)

	// No more pending requests
	_, err = svc.ConsumeFirstPending(ctx, auth, "")
	require.ErrorIs(t, err, ErrNoPendingRequests)
}

// TestServiceConsumeFirstPendingEmpty verifies that ConsumeFirstPending
// returns ErrNoPendingRequests when there are no pending requests.
func TestServiceConsumeFirstPendingEmpty(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	auth := testAuth("hash-empty", "efgh")
	ctx := context.Background()

	// No requests exist
	_, err = svc.ConsumeFirstPending(ctx, auth, "")
	require.ErrorIs(t, err, ErrNoPendingRequests)
}

// TestServiceConsumeFirstPendingIsolation verifies that ConsumeFirstPending
// only returns requests belonging to the authenticated user.
func TestServiceConsumeFirstPendingIsolation(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	authA := testAuth("hash-iso-a", "aaaa")
	authB := testAuth("hash-iso-b", "bbbb")
	ctx := context.Background()

	// Create requests for both users
	reqA, err := svc.CreateRequest(ctx, authA, "User A directive", "")
	require.NoError(t, err)

	_, err = svc.CreateRequest(ctx, authB, "User B directive", "")
	require.NoError(t, err)

	// User A should only get their own request
	consumedA, err := svc.ConsumeFirstPending(ctx, authA, "")
	require.NoError(t, err)
	require.Equal(t, reqA.ID, consumedA.ID)
	require.Equal(t, "User A directive", consumedA.Content)

	// User A has no more pending requests
	_, err = svc.ConsumeFirstPending(ctx, authA, "")
	require.ErrorIs(t, err, ErrNoPendingRequests)

	// User B should still have their request
	consumedB, err := svc.ConsumeFirstPending(ctx, authB, "")
	require.NoError(t, err)
	require.Equal(t, "User B directive", consumedB.Content)
}

func TestServiceTaskIsolation(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	auth := testAuth("hash-task", "abcd")
	ctx := context.Background()

	defaultReq, err := svc.CreateRequest(ctx, auth, "Default task directive", "")
	require.NoError(t, err)
	otherTaskReq, err := svc.CreateRequest(ctx, auth, "Isolated directive", "task-b")
	require.NoError(t, err)

	consumed, err := svc.ConsumeFirstPending(ctx, auth, "task-b")
	require.NoError(t, err)
	require.Equal(t, otherTaskReq.ID, consumed.ID)

	pending, _, err := svc.ListRequests(ctx, auth, "")
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, defaultReq.ID, pending[0].ID)
}

// TestServiceUserPreferences verifies that user preferences can be stored and retrieved.
func TestServiceUserPreferences(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	auth := testAuth("hash-pref", "abcd")
	ctx := context.Background()

	// No preference set initially - should return default
	mode, err := svc.GetReturnMode(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, DefaultReturnMode, mode)

	// Set preference to "first"
	pref, err := svc.SetReturnMode(ctx, auth, ReturnModeFirst)
	require.NoError(t, err)
	require.Equal(t, ReturnModeFirst, pref.Preferences.ReturnMode)

	// Retrieve preference - should be "first"
	mode, err = svc.GetReturnMode(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, ReturnModeFirst, mode)

	// Update preference to "all"
	pref, err = svc.SetReturnMode(ctx, auth, ReturnModeAll)
	require.NoError(t, err)
	require.Equal(t, ReturnModeAll, pref.Preferences.ReturnMode)

	// Verify update persisted
	mode, err = svc.GetReturnMode(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, ReturnModeAll, mode)
}

// TestServiceUserPreferencesIsolation verifies that user preferences are isolated per user.
func TestServiceUserPreferencesIsolation(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	authA := testAuth("hash-pref-a", "aaaa")
	authB := testAuth("hash-pref-b", "bbbb")
	ctx := context.Background()

	// User A sets preference to "first"
	_, err = svc.SetReturnMode(ctx, authA, ReturnModeFirst)
	require.NoError(t, err)

	// User B sets preference to "all"
	_, err = svc.SetReturnMode(ctx, authB, ReturnModeAll)
	require.NoError(t, err)

	// Verify isolation
	modeA, err := svc.GetReturnMode(ctx, authA)
	require.NoError(t, err)
	require.Equal(t, ReturnModeFirst, modeA)

	modeB, err := svc.GetReturnMode(ctx, authB)
	require.NoError(t, err)
	require.Equal(t, ReturnModeAll, modeB)
}

// TestServiceUserPreferencesInvalidMode verifies that invalid return modes are rejected.
func TestServiceUserPreferencesInvalidMode(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	auth := testAuth("hash-invalid", "abcd")
	ctx := context.Background()

	// Try to set invalid mode
	_, err = svc.SetReturnMode(ctx, auth, "invalid_mode")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid return_mode")
}

// TestValidateReturnMode verifies the ValidateReturnMode helper function.
func TestValidateReturnMode(t *testing.T) {
	require.Equal(t, ReturnModeAll, ValidateReturnMode(""))
	require.Equal(t, ReturnModeAll, ValidateReturnMode("all"))
	require.Equal(t, ReturnModeFirst, ValidateReturnMode("first"))
	require.Equal(t, DefaultReturnMode, ValidateReturnMode("invalid"))
}

// TestServiceReturnModeRawDBVerification verifies return_mode is correctly persisted at DB level.
// This catches issues where the in-memory struct is correct but DB write fails silently.
func TestServiceReturnModeRawDBVerification(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	auth := testAuth("hash-raw-verify", "1234")
	ctx := context.Background()

	// Set preference to "first"
	_, err = svc.SetReturnMode(ctx, auth, ReturnModeFirst)
	require.NoError(t, err)

	// Verify at raw DB level - read the preferences column directly
	var rawPref string
	err = db.Raw("SELECT preferences FROM mcp_user_preferences WHERE api_key_hash = ?", auth.APIKeyHash).Scan(&rawPref).Error
	require.NoError(t, err)
	require.Contains(t, rawPref, `"return_mode":"first"`, "preferences column should contain first mode")

	// Now update to "all" and verify
	_, err = svc.SetReturnMode(ctx, auth, ReturnModeAll)
	require.NoError(t, err)

	err = db.Raw("SELECT preferences FROM mcp_user_preferences WHERE api_key_hash = ?", auth.APIKeyHash).Scan(&rawPref).Error
	require.NoError(t, err)
	require.Contains(t, rawPref, `"return_mode":"all"`, "preferences column should contain all mode after update")
}

// TestServiceReturnModePersistenceAcrossServiceInstances verifies preference survives service restart.
// This simulates what happens when the server restarts or a new request comes in.
func TestServiceReturnModePersistenceAcrossServiceInstances(t *testing.T) {
	db := newTestDB(t)
	auth := testAuth("hash-persist", "abcd")
	ctx := context.Background()

	// First service instance sets preference
	svc1, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	_, err = svc1.SetReturnMode(ctx, auth, ReturnModeFirst)
	require.NoError(t, err)

	// Verify with same instance
	mode, err := svc1.GetReturnMode(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, ReturnModeFirst, mode)

	// Create a NEW service instance (simulates server restart)
	svc2, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	// Verify preference persisted across instances
	mode, err = svc2.GetReturnMode(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, ReturnModeFirst, mode, "preference should persist across service instances")
}
