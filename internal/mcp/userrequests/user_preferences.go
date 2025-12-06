package userrequests

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/Laisky/zap"
	"gorm.io/gorm"

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
}

var prefLogger = log.Logger.Named("user_preferences")

// Value implements driver.Valuer for database serialization.
func (p PreferenceData) Value() (driver.Value, error) {
	return json.Marshal(p)
}

// Scan implements sql.Scanner for database deserialization.
func (p *PreferenceData) Scan(value any) error {
	if value == nil {
		*p = PreferenceData{}
		return nil
	}

	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return errors.Errorf("unsupported type for PreferenceData: %T", value)
	}

	raw := strings.TrimSpace(string(bytes))
	if len(raw) == 0 {
		*p = PreferenceData{}
		return nil
	}

	decodeErr := json.Unmarshal(bytes, p)
	if decodeErr == nil {
		return nil
	}

	// Legacy path: some historical rows stored the JSON as an escaped string
	// (leading backslashes) or as a quoted value. Try to recover before
	// falling back to defaults so users are not blocked by legacy data.
	var unquoted string
	if err := json.Unmarshal(bytes, &unquoted); err == nil {
		// The payload was a JSON string; try to unmarshal that string as JSON
		if err := json.Unmarshal([]byte(unquoted), p); err == nil {
			prefLogger.Debug("recovered preference from quoted JSON")
			return nil
		}

		mode := ValidateReturnMode(strings.TrimSpace(unquoted))
		p.ReturnMode = mode
		prefLogger.Debug("recovered preference from quoted string", zap.String("return_mode", p.ReturnMode))
		return nil
	}

	// Another legacy form stored a backslash-escaped JSON fragment without wrapping braces.
	cleaned := strings.ReplaceAll(raw, "\\", "")
	if err := json.Unmarshal([]byte(cleaned), p); err == nil {
		prefLogger.Debug("recovered preference from escaped JSON", zap.String("return_mode", p.ReturnMode))
		return nil
	}

	if strings.Contains(cleaned, "return_mode") {
		candidate := strings.TrimSpace(cleaned)
		if !strings.HasPrefix(candidate, "{") {
			candidate = "{" + candidate
		}
		if !strings.HasSuffix(candidate, "}") {
			candidate += "}"
		}
		if err := json.Unmarshal([]byte(candidate), p); err == nil {
			prefLogger.Debug("recovered preference from wrapped legacy fragment", zap.String("return_mode", p.ReturnMode))
			return nil
		}
	}

	trimmed := strings.Trim(cleaned, "\"")
	p.ReturnMode = ValidateReturnMode(strings.TrimSpace(trimmed))
	prefLogger.Debug("preference data invalid, defaulting", zap.Error(decodeErr), zap.String("return_mode", p.ReturnMode))
	return nil
}

// UserPreference stores per-user configuration for the MCP user requests feature.
type UserPreference struct {
	APIKeyHash   string         `gorm:"type:char(64);primaryKey"`
	KeySuffix    string         `gorm:"type:varchar(16);not null"`
	UserIdentity string         `gorm:"type:varchar(255);not null"`
	Preferences  PreferenceData `gorm:"type:text;not null"`
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
	err := s.db.WithContext(ctx).
		Where("api_key_hash = ?", auth.APIKeyHash).
		First(&pref).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errors.Wrap(err, "get user preference")
	}

	return &pref, nil
}

// GetReturnMode retrieves the return_mode preference for the authenticated user.
// Returns DefaultReturnMode if no preference is stored.
func (s *Service) GetReturnMode(ctx context.Context, auth *askuser.AuthorizationContext) (string, error) {
	pref, err := s.GetUserPreference(ctx, auth)
	if err != nil {
		return "", err
	}
	if pref == nil || pref.Preferences.ReturnMode == "" {
		return DefaultReturnMode, nil
	}
	return pref.Preferences.ReturnMode, nil
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

	s.log().Debug("set user return_mode preference",
		zap.String("user", auth.UserIdentity),
		zap.String("return_mode", mode),
	)

	now := s.clock()
	pref := &UserPreference{
		APIKeyHash:   auth.APIKeyHash,
		KeySuffix:    auth.KeySuffix,
		UserIdentity: auth.UserIdentity,
		Preferences:  PreferenceData{ReturnMode: mode},
		UpdatedAt:    now,
	}

	// Use upsert to handle both create and update
	err := s.db.WithContext(ctx).
		Where("api_key_hash = ?", auth.APIKeyHash).
		Assign(map[string]any{
			"preferences":   PreferenceData{ReturnMode: mode},
			"key_suffix":    auth.KeySuffix,
			"user_identity": auth.UserIdentity,
			"updated_at":    now,
		}).
		FirstOrCreate(pref).Error
	if err != nil {
		return nil, errors.Wrap(err, "set return mode preference")
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
