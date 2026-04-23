package userrequests

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestPreferenceDataValueSerializesCorrectly verifies that PreferenceData serializes to valid JSON.
func TestPreferenceDataValueSerializesCorrectly(t *testing.T) {
	pref := PreferenceData{ReturnMode: ReturnModeFirst, DisabledTools: []string{"web_fetch"}}
	val, err := pref.Value()
	require.NoError(t, err)

	// Should serialize to JSON bytes
	bytes, ok := val.([]byte)
	require.True(t, ok, "Value() should return []byte")

	// Verify the JSON is valid and has expected structure
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(bytes, &parsed))
	require.Equal(t, "first", parsed["return_mode"])
	require.Equal(t, []any{"web_fetch"}, parsed["disabled_tools"])
}

// TestPreferenceDataScanValidJSON ensures valid JSON is parsed correctly.
func TestPreferenceDataScanValidJSON(t *testing.T) {
	var pref PreferenceData
	err := pref.Scan(`{"return_mode":"first","disabled_tools":["file_write","file_read"]}`)
	require.NoError(t, err)
	require.Equal(t, ReturnModeFirst, pref.ReturnMode)
	require.Equal(t, []string{"file_write", "file_read"}, pref.DisabledTools)
}

// TestNormalizeDisabledTools verifies normalization trims blanks and removes duplicates.
func TestNormalizeDisabledTools(t *testing.T) {
	normalized := NormalizeDisabledTools([]string{" file_read ", "", "file_read", "file_write"})
	require.Equal(t, []string{"file_read", "file_write"}, normalized)
}

// TestServiceSetAndGetDisabledTools verifies disabled tool preferences are persisted per user.
func TestServiceSetAndGetDisabledTools(t *testing.T) {
	db := newTestDB(t)
	clock := fixedClock(time.Date(2024, 12, 4, 0, 0, 0, 0, time.UTC))
	svc, err := NewService(db, nil, clock.Now, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	auth := testAuth("hash-disabled-tools", "abcd")
	ctx := context.Background()

	_, err = svc.SetDisabledTools(ctx, auth, []string{"file_write", "file_search", "file_write"})
	require.NoError(t, err)

	disabledTools, err := svc.GetDisabledTools(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, []string{"file_write", "file_search"}, disabledTools)
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
	_, execErr := db.Exec(
		`INSERT INTO mcp_user_preferences (api_key_hash, key_suffix, user_identity, preferences, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
		auth.APIKeyHash, auth.KeySuffix, auth.UserIdentity, legacyPref, now, now,
	)
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
	_, execErr := db.Exec(
		`INSERT INTO mcp_user_preferences (api_key_hash, key_suffix, user_identity, preferences, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
		auth.APIKeyHash, auth.KeySuffix, auth.UserIdentity, legacyPref, now, now,
	)
	require.NoError(t, execErr)

	mode, err := svc.GetReturnMode(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, ReturnModeFirst, mode)
}

// TestValidateCommandTemplate exercises the validation rules for command_template.
func TestValidateCommandTemplate(t *testing.T) {
	// Empty template is allowed (reset to default).
	require.NoError(t, ValidateCommandTemplate(""))

	// Non-empty template must contain "{{content}}".
	require.NoError(t, ValidateCommandTemplate("wrap: {{content}} end"))
	err := ValidateCommandTemplate("no placeholder here")
	require.Error(t, err)
	require.Contains(t, err.Error(), "{{content}}")

	// Template over 4096 runes should be rejected. Build a runestring slightly over the limit.
	over := make([]rune, CommandTemplateMaxRunes+1)
	for i := range over {
		over[i] = 'a'
	}
	// Include placeholder so only length triggers failure.
	longWithPlaceholder := string(over) + CommandTemplatePlaceholder
	err = ValidateCommandTemplate(longWithPlaceholder)
	require.Error(t, err)
	require.Contains(t, err.Error(), "maximum length")

	// Exactly at the max with placeholder should pass.
	base := make([]rune, CommandTemplateMaxRunes-len([]rune(CommandTemplatePlaceholder)))
	for i := range base {
		base[i] = 'a'
	}
	exact := string(base) + CommandTemplatePlaceholder
	require.NoError(t, ValidateCommandTemplate(exact))

	// Multi-byte rune counting: each emoji counts as a single rune, not multiple bytes.
	emoji := strings.Repeat("\xf0\x9f\x98\x80", CommandTemplateMaxRunes-len([]rune(CommandTemplatePlaceholder)))
	require.NoError(t, ValidateCommandTemplate(emoji+CommandTemplatePlaceholder))
}

// TestRenderCommandTemplate verifies substitution semantics.
func TestRenderCommandTemplate(t *testing.T) {
	// Empty template returns content verbatim (byte-identical behavior).
	require.Equal(t, "hello", RenderCommandTemplate("", "hello"))
	// Non-empty template replaces every occurrence of the placeholder.
	require.Equal(t, "before: hello :after", RenderCommandTemplate("before: {{content}} :after", "hello"))
	require.Equal(t, "x|x", RenderCommandTemplate("{{content}}|{{content}}", "x"))
	// No placeholder in template still renders the template literally (validation ensures
	// stored templates always contain the placeholder, but Render itself is tolerant).
	require.Equal(t, "literal", RenderCommandTemplate("literal", "ignored"))
}

// TestServiceSetAndGetCommandTemplate verifies the template is persisted and round-trips cleanly.
func TestServiceSetAndGetCommandTemplate(t *testing.T) {
	db := newTestDB(t)
	clock := fixedClock(time.Date(2024, 12, 10, 0, 0, 0, 0, time.UTC))
	svc, err := NewService(db, nil, clock.Now, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	auth := testAuth("hash-command-template", "abcd")
	ctx := context.Background()

	// Initial Get returns empty template (no record).
	got, err := svc.GetCommandTemplate(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, "", got)

	// Set and retrieve a valid template.
	template := "User said: {{content}} (end)"
	_, err = svc.SetCommandTemplate(ctx, auth, template)
	require.NoError(t, err)

	got, err = svc.GetCommandTemplate(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, template, got)

	// Clearing with an empty string should persist empty.
	_, err = svc.SetCommandTemplate(ctx, auth, "")
	require.NoError(t, err)
	got, err = svc.GetCommandTemplate(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, "", got)
}

// TestServiceSetCommandTemplateRejectsInvalid verifies that validation errors are surfaced.
func TestServiceSetCommandTemplateRejectsInvalid(t *testing.T) {
	db := newTestDB(t)
	clock := fixedClock(time.Date(2024, 12, 11, 0, 0, 0, 0, time.UTC))
	svc, err := NewService(db, nil, clock.Now, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	auth := testAuth("hash-command-template-invalid", "abcd")
	ctx := context.Background()

	// Missing placeholder.
	_, err = svc.SetCommandTemplate(ctx, auth, "no placeholder here")
	require.Error(t, err)
	require.Contains(t, err.Error(), "{{content}}")

	// Too long.
	over := strings.Repeat("a", CommandTemplateMaxRunes+1) + CommandTemplatePlaceholder
	_, err = svc.SetCommandTemplate(ctx, auth, over)
	require.Error(t, err)
	require.Contains(t, err.Error(), "maximum length")
}

// TestServiceSetReturnModePreservesCommandTemplate ensures SetReturnMode does not wipe an existing template.
func TestServiceSetReturnModePreservesCommandTemplate(t *testing.T) {
	db := newTestDB(t)
	clock := fixedClock(time.Date(2024, 12, 12, 0, 0, 0, 0, time.UTC))
	svc, err := NewService(db, nil, clock.Now, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)

	auth := testAuth("hash-preserve", "abcd")
	ctx := context.Background()

	template := "wrap: {{content}}"
	_, err = svc.SetCommandTemplate(ctx, auth, template)
	require.NoError(t, err)

	_, err = svc.SetReturnMode(ctx, auth, ReturnModeFirst)
	require.NoError(t, err)

	// Template should still be present.
	got, err := svc.GetCommandTemplate(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, template, got)

	// And disabled tools mutation should also preserve it.
	_, err = svc.SetDisabledTools(ctx, auth, []string{"tool_x"})
	require.NoError(t, err)
	got, err = svc.GetCommandTemplate(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, template, got)
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
	_, execErr := db.Exec(
		`INSERT INTO mcp_user_preferences (api_key_hash, key_suffix, user_identity, preferences, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
		auth.APIKeyHash, auth.KeySuffix, auth.UserIdentity, legacyPref, now, now,
	)
	require.NoError(t, execErr)

	mode, err := svc.GetReturnMode(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, ReturnModeFirst, mode)
}
