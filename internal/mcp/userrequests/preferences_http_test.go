package userrequests

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// TestPreferencesHTTPSetAndGet verifies the HTTP preferences endpoint correctly
// persists and retrieves user preferences including return_mode.
func TestPreferencesHTTPSetAndGet(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	handler := NewCombinedHTTPHandler(svc, nil, log.Logger.Named("test"), func() []string {
		return []string{"file_read", "file_write"}
	})

	// Test API key for authorization
	apiKey := "sk-test1234567890abcdef"
	authHeader := "Bearer " + apiKey

	// Step 1: GET should return default mode when no preference is set
	req := httptest.NewRequest(http.MethodGet, "/api/preferences", nil)
	req.Header.Set("Authorization", authHeader)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "GET should succeed")

	var getResp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &getResp))
	require.Equal(t, "all", getResp["return_mode"], "default should be 'all'")
	require.Equal(t, []any{"file_read", "file_write"}, getResp["available_tools"])

	// Step 2: PUT to set mode to "first"
	body := bytes.NewBufferString(`{"return_mode": "first"}`)
	req = httptest.NewRequest(http.MethodPut, "/api/preferences", body)
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "PUT should succeed")

	var setResp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &setResp))
	require.Equal(t, "first", setResp["return_mode"], "response should confirm 'first'")
	require.Equal(t, []any{}, setResp["disabled_tools"])

	// Step 3: GET again to verify persistence
	req = httptest.NewRequest(http.MethodGet, "/api/preferences", nil)
	req.Header.Set("Authorization", authHeader)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "GET after SET should succeed")

	var getResp2 map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &getResp2))
	require.Equal(t, "first", getResp2["return_mode"], "GET should return persisted 'first'")
	require.Equal(t, []any{"file_read", "file_write"}, getResp2["available_tools"])

	// Step 4: PUT to change back to "all"
	body = bytes.NewBufferString(`{"return_mode": "all"}`)
	req = httptest.NewRequest(http.MethodPut, "/api/preferences", body)
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "PUT to 'all' should succeed")

	// Step 5: Verify the update persisted
	req = httptest.NewRequest(http.MethodGet, "/api/preferences", nil)
	req.Header.Set("Authorization", authHeader)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "final GET should succeed")

	var getResp3 map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &getResp3))
	require.Equal(t, "all", getResp3["return_mode"], "GET should return updated 'all'")
	require.Equal(t, []any{}, getResp3["disabled_tools"])
}

// TestPreferencesHTTPSetDisabledTools verifies disabled tools can be updated independently.
func TestPreferencesHTTPSetDisabledTools(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	handler := NewCombinedHTTPHandler(svc, nil, log.Logger.Named("test"), func() []string {
		return []string{"file_read", "file_write", "file_list"}
	})

	authHeader := "Bearer sk-test1234567890abcdef"
	body := bytes.NewBufferString(`{"disabled_tools": ["file_write", "file_list"]}`)
	req := httptest.NewRequest(http.MethodPut, "/api/preferences", body)
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var setResp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &setResp))
	require.Equal(t, []any{"file_write", "file_list"}, setResp["disabled_tools"])
	require.Equal(t, "all", setResp["return_mode"])

	req = httptest.NewRequest(http.MethodGet, "/api/preferences", nil)
	req.Header.Set("Authorization", authHeader)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var getResp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &getResp))
	require.Equal(t, []any{"file_write", "file_list"}, getResp["disabled_tools"])
}

// TestPreferencesHTTPInvalidMode verifies invalid mode values are rejected.
func TestPreferencesHTTPInvalidMode(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	handler := NewCombinedHTTPHandler(svc, nil, log.Logger.Named("test"), nil)

	apiKey := "sk-test1234567890abcdef"
	authHeader := "Bearer " + apiKey

	// Try to set invalid mode
	body := bytes.NewBufferString(`{"return_mode": "invalid"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/preferences", body)
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, "invalid mode should be rejected")
}

// TestPreferencesHTTPMissingAuth verifies unauthorized requests are rejected.
func TestPreferencesHTTPMissingAuth(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	handler := NewCombinedHTTPHandler(svc, nil, log.Logger.Named("test"), nil)

	// GET without auth
	req := httptest.NewRequest(http.MethodGet, "/api/preferences", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code, "missing auth should be rejected")
}

// TestPreferencesHTTPCommandTemplate verifies command_template round-trips and
// that presence-aware decoding lets callers clear the stored value.
func TestPreferencesHTTPCommandTemplate(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	handler := NewCombinedHTTPHandler(svc, nil, log.Logger.Named("test"), nil)
	authHeader := "Bearer sk-template-abc1234567890def"

	// GET on an untouched account returns empty template.
	req := httptest.NewRequest(http.MethodGet, "/api/preferences", nil)
	req.Header.Set("Authorization", authHeader)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var getResp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &getResp))
	require.Equal(t, "", getResp["command_template"], "default command_template should be empty")

	// PUT sets a valid template.
	body := bytes.NewBufferString(`{"command_template": "User says: {{content}}"}`)
	req = httptest.NewRequest(http.MethodPut, "/api/preferences", body)
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "setting template should succeed; body=%s", rec.Body.String())
	var setResp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &setResp))
	require.Equal(t, "User says: {{content}}", setResp["command_template"])

	// GET confirms persistence.
	req = httptest.NewRequest(http.MethodGet, "/api/preferences", nil)
	req.Header.Set("Authorization", authHeader)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var getResp2 map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &getResp2))
	require.Equal(t, "User says: {{content}}", getResp2["command_template"])

	// PUT without command_template key must NOT clobber existing value (presence-aware decoding).
	body = bytes.NewBufferString(`{"return_mode": "first"}`)
	req = httptest.NewRequest(http.MethodPut, "/api/preferences", body)
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	req = httptest.NewRequest(http.MethodGet, "/api/preferences", nil)
	req.Header.Set("Authorization", authHeader)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var getResp3 map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &getResp3))
	require.Equal(t, "User says: {{content}}", getResp3["command_template"], "omitted key must leave template untouched")
	require.Equal(t, "first", getResp3["return_mode"])

	// PUT with explicit empty string clears the template.
	body = bytes.NewBufferString(`{"command_template": ""}`)
	req = httptest.NewRequest(http.MethodPut, "/api/preferences", body)
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var setRespCleared map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &setRespCleared))
	require.Equal(t, "", setRespCleared["command_template"])
}

// TestPreferencesHTTPCommandTemplateValidationErrors verifies invalid templates
// are rejected with HTTP 400 and a JSON error body.
func TestPreferencesHTTPCommandTemplateValidationErrors(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	handler := NewCombinedHTTPHandler(svc, nil, log.Logger.Named("test"), nil)
	authHeader := "Bearer sk-template-invalid1234567"

	// Missing placeholder rejected.
	body := bytes.NewBufferString(`{"command_template": "no placeholder here"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/preferences", body)
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	var errResp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	require.Contains(t, errResp["error"], "{{content}}")

	// Oversized template rejected.
	tooLong := `"` + "wrap: {{content}} "
	for i := 0; i < CommandTemplateMaxRunes+10; i++ {
		tooLong += "a"
	}
	tooLong += `"`
	body = bytes.NewBufferString(`{"command_template": ` + tooLong + `}`)
	req = httptest.NewRequest(http.MethodPut, "/api/preferences", body)
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	require.Contains(t, errResp["error"], "maximum length")
}

// TestPreferencesHTTPUserIsolation verifies preferences are isolated per user.
func TestPreferencesHTTPUserIsolation(t *testing.T) {
	db := newTestDB(t)
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	handler := NewCombinedHTTPHandler(svc, nil, log.Logger.Named("test"), nil)

	// Two different users
	userA := "Bearer sk-userA1234567890abc"
	userB := "Bearer sk-userB9876543210xyz"

	// User A sets mode to "first"
	body := bytes.NewBufferString(`{"return_mode": "first"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/preferences", body)
	req.Header.Set("Authorization", userA)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	// User B sets mode to "all"
	body = bytes.NewBufferString(`{"return_mode": "all"}`)
	req = httptest.NewRequest(http.MethodPut, "/api/preferences", body)
	req.Header.Set("Authorization", userB)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	// Verify User A still has "first"
	req = httptest.NewRequest(http.MethodGet, "/api/preferences", nil)
	req.Header.Set("Authorization", userA)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var respA map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respA))
	require.Equal(t, "first", respA["return_mode"], "User A should have 'first'")

	// Verify User B has "all"
	req = httptest.NewRequest(http.MethodGet, "/api/preferences", nil)
	req.Header.Set("Authorization", userB)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var respB map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respB))
	require.Equal(t, "all", respB["return_mode"], "User B should have 'all'")
}
