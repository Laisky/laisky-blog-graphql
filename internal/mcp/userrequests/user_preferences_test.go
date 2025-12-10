package userrequests

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestPreferenceDataValueSerializesCorrectly verifies that PreferenceData serializes to valid JSON.
func TestPreferenceDataValueSerializesCorrectly(t *testing.T) {
	pref := PreferenceData{ReturnMode: ReturnModeFirst}
	val, err := pref.Value()
	require.NoError(t, err)

	// Should serialize to JSON bytes
	bytes, ok := val.([]byte)
	require.True(t, ok, "Value() should return []byte")

	// Verify the JSON is valid and has expected structure
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(bytes, &parsed))
	require.Equal(t, "first", parsed["return_mode"])
}

// TestPreferenceDataScanValidJSON ensures valid JSON is parsed correctly.
func TestPreferenceDataScanValidJSON(t *testing.T) {
	var pref PreferenceData
	err := pref.Scan(`{"return_mode":"first"}`)
	require.NoError(t, err)
	require.Equal(t, ReturnModeFirst, pref.ReturnMode)
}

// TestPreferenceDataScanLegacyEscapedString ensures legacy escaped preference payloads are recovered.
func TestPreferenceDataScanLegacyEscapedString(t *testing.T) {
	var pref PreferenceData
	err := pref.Scan(`\\"return_mode\\":\\"first\\"`)
	require.NoError(t, err)
	require.Equal(t, ReturnModeFirst, pref.ReturnMode)
}

// TestPreferenceDataScanLegacyQuotedMode ensures quoted string values are handled gracefully.
func TestPreferenceDataScanLegacyQuotedMode(t *testing.T) {
	var pref PreferenceData
	err := pref.Scan(`"first"`)
	require.NoError(t, err)
	require.Equal(t, ReturnModeFirst, pref.ReturnMode)
}

// TestPreferenceDataScanLegacyEscapedObjects covers multiple historical payload encodings.
func TestPreferenceDataScanLegacyEscapedObjects(t *testing.T) {
	testCases := []struct {
		name  string
		value any
	}{
		{name: "LeadingBackslash", value: []byte(`\{"return_mode":"first"}`)},
		{name: "QuotedEscaped", value: `"{\"return_mode\":\"first\"}"`},
		{name: "DoubleEscaped", value: `"\\\"return_mode\\\":\\\"first\\\""`},
		{name: "HexEncoded", value: []byte(`\x7b2272657475726e5f6d6f6465223a226669727374227d`)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var pref PreferenceData
			require.NoError(t, pref.Scan(tc.value))
			require.Equal(t, ReturnModeFirst, pref.ReturnMode)
		})
	}
}

// TestServiceSetReturnModeRecoversLegacyPreference verifies SetReturnMode cleans up legacy rows.
func TestServiceSetReturnModeRecoversLegacyPreference(t *testing.T) {
	db := newTestDB(t)
	clock := fixedClock(time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC))
	svc, err := NewService(db, nil, clock.Now, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	auth := testAuth("hash-legacy", "zzzz")
	ctx := context.Background()

	legacyPref := `\\"return_mode\\":\\"first\\"`
	now := clock.Now()
	execErr := db.Exec(
		`INSERT INTO mcp_user_preferences (api_key_hash, key_suffix, user_identity, preferences, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
		auth.APIKeyHash, auth.KeySuffix, auth.UserIdentity, legacyPref, now, now,
	).Error
	require.NoError(t, execErr)

	mode, err := svc.GetReturnMode(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, ReturnModeFirst, mode)

	pref, err := svc.SetReturnMode(ctx, auth, ReturnModeAll)
	require.NoError(t, err)
	require.Equal(t, ReturnModeAll, pref.Preferences.ReturnMode)

	mode, err = svc.GetReturnMode(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, ReturnModeAll, mode)
}

// TestServiceGetReturnModeHandlesEscapedObject verifies we can read rows stored with a leading backslash.
func TestServiceGetReturnModeHandlesEscapedObject(t *testing.T) {
	db := newTestDB(t)
	clock := fixedClock(time.Date(2024, 12, 2, 0, 0, 0, 0, time.UTC))
	svc, err := NewService(db, nil, clock.Now, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	auth := testAuth("hash-escaped", "zzzz")
	ctx := context.Background()
	legacyPref := `\{"return_mode":"first"}`
	now := clock.Now()
	execErr := db.Exec(
		`INSERT INTO mcp_user_preferences (api_key_hash, key_suffix, user_identity, preferences, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
		auth.APIKeyHash, auth.KeySuffix, auth.UserIdentity, legacyPref, now, now,
	).Error
	require.NoError(t, execErr)

	mode, err := svc.GetReturnMode(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, ReturnModeFirst, mode)
}

func TestServiceGetReturnModeHandlesHexEncodedPreference(t *testing.T) {
	db := newTestDB(t)
	clock := fixedClock(time.Date(2024, 12, 3, 0, 0, 0, 0, time.UTC))
	svc, err := NewService(db, nil, clock.Now, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	auth := testAuth("hash-hex", "zzzz")
	ctx := context.Background()
	legacyPref := `\x7b2272657475726e5f6d6f6465223a226669727374227d`
	now := clock.Now()
	execErr := db.Exec(
		`INSERT INTO mcp_user_preferences (api_key_hash, key_suffix, user_identity, preferences, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
		auth.APIKeyHash, auth.KeySuffix, auth.UserIdentity, legacyPref, now, now,
	).Error
	require.NoError(t, execErr)

	mode, err := svc.GetReturnMode(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, ReturnModeFirst, mode)
}
