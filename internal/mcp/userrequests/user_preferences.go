package userrequests

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

const (
	// ReturnModeAll returns all pending commands in FIFO order.
	ReturnModeAll = "all"
	// ReturnModeFirst returns only the oldest (first) pending command.
	ReturnModeFirst = "first"
	// DefaultReturnMode is used when no preference is set.
	DefaultReturnMode = ReturnModeAll
)

// PreferenceData holds the JSON-serializable user preferences.
// Add new preference fields here for future extensibility.
type PreferenceData struct {
	// ReturnMode determines how the get_user_request tool returns pending commands.
	// Valid values: "all" (default), "first"
	ReturnMode string `json:"return_mode,omitempty"`
	// DisabledTools stores MCP tool names explicitly disabled by the user.
	DisabledTools []string `json:"disabled_tools,omitempty"`
}

// DefaultDisabledTools provides the default disabled tool list when no preference exists.
var DefaultDisabledTools = []string{}

var prefLogger = log.Logger.Named("user_preferences")

// Value implements driver.Valuer for database serialization.
func (p PreferenceData) Value() (driver.Value, error) {
	value, err := json.Marshal(p)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return value, nil
}

// Scan implements sql.Scanner for database deserialization.
func (p *PreferenceData) Scan(value any) error {
	if value == nil {
		*p = PreferenceData{}
		return nil
	}

	rawBytes, err := bytesFromDBValue(value)
	if err != nil {
		return errors.WithStack(err)
	}

	trimmed := bytes.TrimSpace(rawBytes)
	if len(trimmed) == 0 {
		*p = PreferenceData{}
		return nil
	}

	normalized, recovered, normErr := normalizePreferencePayload(trimmed)
	if normErr != nil {
		prefLogger.Debug("preference data invalid, defaulting",
			zap.Error(normErr),
			zap.String("raw_preview", preferencePreview(trimmed)),
		)
		p.ReturnMode = DefaultReturnMode
		return nil
	}

	if recovered {
		prefLogger.Debug("normalized legacy preference payload",
			zap.String("raw_preview", preferencePreview(trimmed)),
			zap.String("normalized_preview", preferencePreview(normalized)),
		)
	}

	if err := json.Unmarshal(normalized, p); err != nil {
		prefLogger.Debug("preference data invalid after normalization",
			zap.Error(err),
			zap.String("raw_preview", preferencePreview(trimmed)),
		)
		p.ReturnMode = DefaultReturnMode
		return nil
	}

	p.ReturnMode = ValidateReturnMode(p.ReturnMode)
	p.DisabledTools = NormalizeDisabledTools(p.DisabledTools)
	return nil
}

// UserPreference stores per-user configuration for the MCP user requests feature.
type UserPreference struct {
	APIKeyHash   string
	KeySuffix    string
	UserIdentity string
	Preferences  PreferenceData
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TableName returns the database table name for user preferences.
func (UserPreference) TableName() string {
	return "mcp_user_preferences"
}

// GetUserPreference retrieves the preference for the authenticated user.
// Returns nil without error if no preference exists (caller should use defaults).
func (s *Service) GetUserPreference(ctx context.Context, auth *askuser.AuthorizationContext) (*UserPreference, error) {
	if auth == nil {
		return nil, ErrInvalidAuthorization
	}

	var pref UserPreference
	var createdAtRaw any
	var updatedAtRaw any
	err := s.queryRowContext(ctx,
		`SELECT api_key_hash, key_suffix, user_identity, preferences, created_at, updated_at
		 FROM mcp_user_preferences
		 WHERE api_key_hash = ?
		 LIMIT 1`,
		auth.APIKeyHash,
	).Scan(
		&pref.APIKeyHash,
		&pref.KeySuffix,
		&pref.UserIdentity,
		&pref.Preferences,
		&createdAtRaw,
		&updatedAtRaw,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, errors.Wrap(err, "get user preference")
	}

	pref.CreatedAt, err = parseSQLTime(createdAtRaw)
	if err != nil {
		return nil, errors.Wrap(err, "parse preference created_at")
	}
	pref.UpdatedAt, err = parseSQLTime(updatedAtRaw)
	if err != nil {
		return nil, errors.Wrap(err, "parse preference updated_at")
	}

	return &pref, nil
}

// GetReturnMode retrieves the return_mode preference for the authenticated user.
// Returns DefaultReturnMode if no preference is stored.
func (s *Service) GetReturnMode(ctx context.Context, auth *askuser.AuthorizationContext) (string, error) {
	pref, err := s.GetUserPreference(ctx, auth)
	if err != nil {
		s.log().Debug("GetReturnMode failed to get preference",
			zap.String("user", auth.UserIdentity),
			zap.Error(err),
		)
		return "", err
	}
	if pref == nil || pref.Preferences.ReturnMode == "" {
		s.log().Debug("GetReturnMode returning default (no preference stored)",
			zap.String("user", auth.UserIdentity),
			zap.Bool("pref_nil", pref == nil),
		)
		return DefaultReturnMode, nil
	}
	s.log().Debug("GetReturnMode returning stored preference",
		zap.String("user", auth.UserIdentity),
		zap.String("return_mode", pref.Preferences.ReturnMode),
	)
	return pref.Preferences.ReturnMode, nil
}

// GetDisabledTools retrieves the disabled MCP tools for the authenticated user.
// Returns DefaultDisabledTools when no preference is stored.
func (s *Service) GetDisabledTools(ctx context.Context, auth *askuser.AuthorizationContext) ([]string, error) {
	pref, err := s.GetUserPreference(ctx, auth)
	if err != nil {
		s.log().Debug("GetDisabledTools failed to get preference",
			zap.String("user", auth.UserIdentity),
			zap.Error(err),
		)
		return nil, err
	}
	if pref == nil {
		s.log().Debug("GetDisabledTools returning default (no preference stored)",
			zap.String("user", auth.UserIdentity),
		)
		return append([]string(nil), DefaultDisabledTools...), nil
	}

	disabled := NormalizeDisabledTools(pref.Preferences.DisabledTools)
	s.log().Debug("GetDisabledTools returning stored preference",
		zap.String("user", auth.UserIdentity),
		zap.Int("disabled_count", len(disabled)),
	)
	return disabled, nil
}

// SetReturnMode updates the return_mode preference for the authenticated user.
// Creates a new preference record if one doesn't exist.
func (s *Service) SetReturnMode(ctx context.Context, auth *askuser.AuthorizationContext, mode string) (*UserPreference, error) {
	if auth == nil {
		return nil, ErrInvalidAuthorization
	}

	// Validate mode
	if mode != ReturnModeAll && mode != ReturnModeFirst {
		return nil, errors.Errorf("invalid return_mode: %s (must be 'all' or 'first')", mode)
	}

	s.log().Debug("SetReturnMode called",
		zap.String("user", auth.UserIdentity),
		zap.String("api_key_hash", auth.APIKeyHash[:8]+"..."),
		zap.String("return_mode", mode),
	)

	now := s.clock()
	disabledTools, getErr := s.GetDisabledTools(ctx, auth)
	if getErr != nil {
		return nil, errors.Wrap(getErr, "load disabled tools")
	}
	pref := &UserPreference{
		APIKeyHash:   auth.APIKeyHash,
		KeySuffix:    auth.KeySuffix,
		UserIdentity: auth.UserIdentity,
		Preferences: PreferenceData{
			ReturnMode:    mode,
			DisabledTools: disabledTools,
		},
		UpdatedAt: now,
	}

	prefPayload, err := pref.Preferences.Value()
	if err != nil {
		return nil, errors.Wrap(err, "marshal return mode preference payload")
	}

	_, err = s.execContext(ctx,
		`INSERT INTO mcp_user_preferences
		 (api_key_hash, key_suffix, user_identity, preferences, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(api_key_hash)
		 DO UPDATE SET
		 	key_suffix = excluded.key_suffix,
		 	user_identity = excluded.user_identity,
		 	preferences = excluded.preferences,
		 	updated_at = excluded.updated_at`,
		auth.APIKeyHash,
		auth.KeySuffix,
		auth.UserIdentity,
		prefPayload,
		now,
		now,
	)
	if err != nil {
		s.log().Error("SetReturnMode database error",
			zap.String("user", auth.UserIdentity),
			zap.Error(err),
		)
		return nil, errors.Wrap(err, "set return mode preference")
	}

	s.log().Debug("SetReturnMode succeeded",
		zap.String("user", auth.UserIdentity),
		zap.String("stored_mode", pref.Preferences.ReturnMode),
	)

	return pref, nil
}

// SetDisabledTools updates the disabled MCP tool preference for the authenticated user.
// Creates a new preference record if one does not exist.
func (s *Service) SetDisabledTools(ctx context.Context, auth *askuser.AuthorizationContext, disabledTools []string) (*UserPreference, error) {
	if auth == nil {
		return nil, ErrInvalidAuthorization
	}

	normalized := NormalizeDisabledTools(disabledTools)
	mode, err := s.GetReturnMode(ctx, auth)
	if err != nil {
		return nil, errors.Wrap(err, "load return mode")
	}

	now := s.clock()
	pref := &UserPreference{
		APIKeyHash:   auth.APIKeyHash,
		KeySuffix:    auth.KeySuffix,
		UserIdentity: auth.UserIdentity,
		Preferences: PreferenceData{
			ReturnMode:    mode,
			DisabledTools: normalized,
		},
		UpdatedAt: now,
	}

	prefPayload, err := pref.Preferences.Value()
	if err != nil {
		return nil, errors.Wrap(err, "marshal disabled tools preference payload")
	}

	_, err = s.execContext(ctx,
		`INSERT INTO mcp_user_preferences
		 (api_key_hash, key_suffix, user_identity, preferences, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(api_key_hash)
		 DO UPDATE SET
		 	key_suffix = excluded.key_suffix,
		 	user_identity = excluded.user_identity,
		 	preferences = excluded.preferences,
		 	updated_at = excluded.updated_at`,
		auth.APIKeyHash,
		auth.KeySuffix,
		auth.UserIdentity,
		prefPayload,
		now,
		now,
	)
	if err != nil {
		return nil, errors.Wrap(err, "set disabled tools preference")
	}

	return pref, nil
}

// ValidateReturnMode checks if the provided mode is valid.
// Returns the mode if valid, or DefaultReturnMode if empty.
func ValidateReturnMode(mode string) string {
	switch mode {
	case ReturnModeFirst:
		return ReturnModeFirst
	case ReturnModeAll, "":
		return ReturnModeAll
	default:
		return DefaultReturnMode
	}
}

// NormalizeDisabledTools sanitizes tool names, removes duplicates, and keeps deterministic order.
func NormalizeDisabledTools(toolNames []string) []string {
	if len(toolNames) == 0 {
		return []string{}
	}

	unique := make(map[string]struct{}, len(toolNames))
	normalized := make([]string, 0, len(toolNames))
	for _, raw := range toolNames {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if _, exists := unique[name]; exists {
			continue
		}
		unique[name] = struct{}{}
		normalized = append(normalized, name)
	}

	if len(normalized) == 0 {
		return []string{}
	}

	return normalized
}

// bytesFromDBValue converts supported driver values into raw byte slices.
func bytesFromDBValue(value any) ([]byte, error) {
	switch v := value.(type) {
	case []byte:
		return v, nil
	case string:
		return []byte(v), nil
	default:
		return nil, errors.Errorf("unsupported type for PreferenceData: %T", value)
	}
}

// normalizePreferencePayload attempts to coerce legacy-encoded preference blobs into valid JSON objects.
// It returns the normalized payload, a flag indicating whether recovery was required, and an error if normalization failed.
func normalizePreferencePayload(raw []byte) ([]byte, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	recovered := false

	hexDecoded, hexRecovered, err := decodeHexPreferencePayload(trimmed)
	if err != nil {
		return nil, false, err
	}
	if hexRecovered {
		trimmed = hexDecoded
		recovered = true
	}

	if len(trimmed) == 0 {
		return []byte("{}"), recovered, nil
	}

	if isPreferenceJSONObject(trimmed) {
		return trimmed, recovered, nil
	}

	current := append([]byte(nil), trimmed...)
	for i := 0; i < 3; i++ {
		if len(current) < 2 || current[0] != '"' || current[len(current)-1] != '"' {
			break
		}
		decoded, err := strconv.Unquote(string(current))
		if err != nil {
			break
		}
		current = bytes.TrimSpace([]byte(decoded))
		if isPreferenceJSONObject(current) {
			return current, true, nil
		}
	}

	withoutSlashes := bytes.ReplaceAll(current, []byte(`\`), nil)
	withoutSlashes = bytes.TrimSpace(withoutSlashes)
	if isPreferenceJSONObject(withoutSlashes) {
		return withoutSlashes, true, nil
	}

	if bytes.Contains(withoutSlashes, []byte("return_mode")) {
		candidate := bytes.TrimSpace(withoutSlashes)
		if len(candidate) > 0 {
			if candidate[0] != '{' {
				candidate = append([]byte("{"), candidate...)
			}
			if candidate[len(candidate)-1] != '}' {
				candidate = append(candidate, '}')
			}
			if isPreferenceJSONObject(candidate) {
				return candidate, true, nil
			}
		}
	}

	bare := strings.Trim(string(bytes.TrimSpace(withoutSlashes)), "\" ")
	if bare != "" {
		mode := ValidateReturnMode(bare)
		if mode != "" {
			payload := []byte(`{"return_mode":"` + mode + `"}`)
			return payload, true, nil
		}
	}

	return nil, recovered, errors.Errorf("unsupported preference payload format")
}

// decodeHexPreferencePayload converts strings encoded as \x<hex> into their JSON equivalents.
func decodeHexPreferencePayload(data []byte) ([]byte, bool, error) {
	if len(data) < 2 {
		return data, false, nil
	}
	if data[0] != '\\' || (data[1] != 'x' && data[1] != 'X') {
		return data, false, nil
	}

	hexPayload := bytes.TrimSpace(data[2:])
	if len(hexPayload) == 0 {
		return []byte{}, true, nil
	}

	decoded := make([]byte, hex.DecodedLen(len(hexPayload)))
	n, err := hex.Decode(decoded, hexPayload)
	if err != nil {
		return nil, false, errors.Wrap(err, "decode hex preference payload")
	}
	plain := bytes.TrimSpace(decoded[:n])
	prefLogger.Debug("decoded hex preference payload",
		zap.String("raw_preview", preferencePreview(data)),
		zap.String("decoded_preview", preferencePreview(plain)),
	)
	return plain, true, nil
}

// isPreferenceJSONObject checks whether data is valid JSON that starts with an object.
func isPreferenceJSONObject(data []byte) bool {
	return json.Valid(data) && len(data) > 0 && data[0] == '{'
}

// preferencePreviewLimit caps the length of preference previews logged.
const preferencePreviewLimit = 64

// preferencePreview returns a short preview string for logging.
func preferencePreview(data []byte) string {
	if len(data) <= preferencePreviewLimit {
		return string(data)
	}
	return string(data[:preferencePreviewLimit]) + "..."
}
