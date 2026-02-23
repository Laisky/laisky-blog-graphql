package userrequests

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"

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

	pending, consumed, total, err := svc.ListRequests(ctx, auth, "", false, "", 0)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Len(t, consumed, 0)
	require.Equal(t, int64(0), total)

	second, err := svc.CreateRequest(ctx, auth, "Second directive", "")
	require.NoError(t, err)
	require.Equal(t, DefaultTaskID, second.TaskID)

	_, _, _, err = svc.ListRequests(ctx, auth, "", false, "", 0)
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

	pending, consumed, total, err = svc.ListRequests(ctx, auth, "", false, "", 0)
	require.NoError(t, err)
	require.Len(t, pending, 0)
	require.Len(t, consumed, 2)
	require.Equal(t, int64(2), total)

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

	customReq, err := svc.CreateRequest(ctx, authA, "Directive custom", "task-x")
	require.NoError(t, err)
	require.NoError(t, svc.DeleteRequest(ctx, authA, customReq.ID, ""))

	deleted, err := svc.DeleteAll(ctx, authB, "", false)
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
	_, err = db.Exec(`UPDATE mcp_user_requests SET created_at = ? WHERE id = ?`, oldCreatedAt, oldReq.ID.String())
	require.NoError(t, err)

	pending, _, _, err := svc.ListRequests(ctx, auth, "task-expired", false, "", 0)
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

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:userrequests_test?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})
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
	pending, _, _, err := svc.ListRequests(ctx, auth, "", false, "", 0)
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

	pending, _, _, err := svc.ListRequests(ctx, auth, "", false, "", 0)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, defaultReq.ID, pending[0].ID)
}

func TestServiceListRequestsAllTasks(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	auth := testAuth("hash-all", "abcd")
	ctx := context.Background()

	firstDefault, err := svc.CreateRequest(ctx, auth, "Default directive", "")
	require.NoError(t, err)
	secondTask, err := svc.CreateRequest(ctx, auth, "Task specific directive", "task-x")
	require.NoError(t, err)

	pendingAll, _, _, err := svc.ListRequests(ctx, auth, "", true, "", 0)
	require.NoError(t, err)
	require.Len(t, pendingAll, 2)
	require.Equal(t, firstDefault.ID, pendingAll[0].ID)
	require.Equal(t, secondTask.ID, pendingAll[1].ID)

	pendingTask, _, _, err := svc.ListRequests(ctx, auth, "task-x", false, "", 0)
	require.NoError(t, err)
	require.Len(t, pendingTask, 1)
	require.Equal(t, secondTask.ID, pendingTask[0].ID)
}

func TestServiceDeleteAllPendingAllTasks(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	auth := testAuth("hash-delete-pending", "1111")
	ctx := context.Background()

	_, err = svc.CreateRequest(ctx, auth, "Default", "")
	require.NoError(t, err)
	otherReq, err := svc.CreateRequest(ctx, auth, "Other", "task-z")
	require.NoError(t, err)

	deletedDefault, err := svc.DeleteAllPending(ctx, auth, "", false)
	require.NoError(t, err)
	require.Equal(t, int64(1), deletedDefault)

	pending, _, _, err := svc.ListRequests(ctx, auth, "", true, "", 0)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, otherReq.ID, pending[0].ID)

	deletedAll, err := svc.DeleteAllPending(ctx, auth, "", true)
	require.NoError(t, err)
	require.Equal(t, int64(1), deletedAll)

	pending, _, _, err = svc.ListRequests(ctx, auth, "", true, "", 0)
	require.NoError(t, err)
	require.Len(t, pending, 0)

	// Verify deleting when nothing pending returns zero but no error
	deletedEmpty, err := svc.DeleteAllPending(ctx, auth, "", true)
	require.NoError(t, err)
	require.Equal(t, int64(0), deletedEmpty)

}

func TestServiceDeleteConsumedAllTasks(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	auth := testAuth("hash-delete-consumed", "2222")
	ctx := context.Background()

	_, err = svc.CreateRequest(ctx, auth, "Default", "")
	require.NoError(t, err)
	_, err = svc.CreateRequest(ctx, auth, "Other", "task-z")
	require.NoError(t, err)

	// Consume both tasks
	_, err = svc.ConsumeFirstPending(ctx, auth, "")
	require.NoError(t, err)
	_, err = svc.ConsumeFirstPending(ctx, auth, "task-z")
	require.NoError(t, err)

	deletedDefault, err := svc.DeleteConsumed(ctx, auth, 0, 0, "", false)
	require.NoError(t, err)
	require.Equal(t, int64(1), deletedDefault)

	deletedAll, err := svc.DeleteConsumed(ctx, auth, 0, 0, "", true)
	require.NoError(t, err)
	require.Equal(t, int64(1), deletedAll)

	deletedEmpty, err := svc.DeleteConsumed(ctx, auth, 0, 0, "", true)
	require.NoError(t, err)
	require.Equal(t, int64(0), deletedEmpty)
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
	err = db.QueryRow("SELECT preferences FROM mcp_user_preferences WHERE api_key_hash = ?", auth.APIKeyHash).Scan(&rawPref)
	require.NoError(t, err)
	require.Contains(t, rawPref, `"return_mode":"first"`, "preferences column should contain first mode")

	// Now update to "all" and verify
	_, err = svc.SetReturnMode(ctx, auth, ReturnModeAll)
	require.NoError(t, err)

	err = db.QueryRow("SELECT preferences FROM mcp_user_preferences WHERE api_key_hash = ?", auth.APIKeyHash).Scan(&rawPref)
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

func TestServiceReorderRequests(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	auth := testAuth("hash-reorder", "1234")
	ctx := context.Background()

	req1, err := svc.CreateRequest(ctx, auth, "Directive 1", "")
	require.NoError(t, err)
	req2, err := svc.CreateRequest(ctx, auth, "Directive 2", "")
	require.NoError(t, err)
	req3, err := svc.CreateRequest(ctx, auth, "Directive 3", "")
	require.NoError(t, err)

	// Initial order should be 1, 2, 3
	pending, _, _, err := svc.ListRequests(ctx, auth, "", false, "", 0)
	require.NoError(t, err)
	require.Len(t, pending, 3)
	require.Equal(t, req1.ID, pending[0].ID)
	require.Equal(t, req2.ID, pending[1].ID)
	require.Equal(t, req3.ID, pending[2].ID)

	// Reorder to 3, 1, 2
	err = svc.ReorderRequests(ctx, auth, []uuid.UUID{req3.ID, req1.ID, req2.ID})
	require.NoError(t, err)

	// Verify new order via ListRequests
	pending, _, _, err = svc.ListRequests(ctx, auth, "", false, "", 0)
	require.NoError(t, err)
	require.Len(t, pending, 3)
	require.Equal(t, req3.ID, pending[0].ID)
	require.Equal(t, req1.ID, pending[1].ID)
	require.Equal(t, req2.ID, pending[2].ID)

	// Verify ConsumeFirstPending follows the new order
	first, err := svc.ConsumeFirstPending(ctx, auth, "")
	require.NoError(t, err)
	require.Equal(t, req3.ID, first.ID, "should consume req3 (the new first)")

	// Verify ConsumeAllPending follows the remaining order
	allRemaining, err := svc.ConsumeAllPending(ctx, auth, "")
	require.NoError(t, err)
	require.Len(t, allRemaining, 2)
	require.Equal(t, req1.ID, allRemaining[0].ID)
	require.Equal(t, req2.ID, allRemaining[1].ID)
}

func TestServiceSearchRequests(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	auth := testAuth("hash-search", "5678")
	ctx := context.Background()

	_, err = svc.CreateRequest(ctx, auth, "Hello world", "")
	require.NoError(t, err)
	_, err = svc.CreateRequest(ctx, auth, "Goodbye moon", "")
	require.NoError(t, err)
	_, err = svc.CreateRequest(ctx, auth, "Fuzzy wuzzy was a bear", "")
	require.NoError(t, err)

	results, err := svc.SearchRequests(ctx, auth, "world", 0)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "Hello world", results[0].Content)

	results, err = svc.SearchRequests(ctx, auth, "oo", 0)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "Goodbye moon", results[0].Content)

	results, err = svc.SearchRequests(ctx, auth, "w", 0)
	require.NoError(t, err)
	require.Len(t, results, 2) // Hello world, Fuzzy wuzzy...

	results, err = svc.SearchRequests(ctx, auth, "nonexistent", 0)
	require.NoError(t, err)
	require.Len(t, results, 0)
}
