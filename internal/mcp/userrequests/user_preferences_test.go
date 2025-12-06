package userrequests

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

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

// TestServiceSetReturnModeRecoversLegacyPreference verifies SetReturnMode cleans up legacy rows.
func TestServiceSetReturnModeRecoversLegacyPreference(t *testing.T) {
	db := newTestDB(t)
	clock := fixedClock(time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC))
	svc, err := NewService(db, nil, clock.Now)
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
