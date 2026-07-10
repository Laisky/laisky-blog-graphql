package oneapi

import (
	"context"
	"encoding/base64"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"gorm.io/gorm"

	blogmodel "github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

// ErrNotFound indicates that an authoritative OneAPI or SSO side-table record
// does not exist.
var ErrNotFound = errors.New("oneapi: record not found")

// Repo provides SSO-safe access to OneAPI-owned user tables and SSO-owned side
// tables in the same database.
type Repo struct {
	db     *gorm.DB
	logger glog.Logger
}

// New constructs a OneAPI user repository over an existing GORM connection.
func New(logger glog.Logger, db *gorm.DB) *Repo {
	return &Repo{db: db, logger: logger}
}

// Prepare validates the authoritative OneAPI schema and idempotently migrates
// only the tables owned by this SSO service.
func (r *Repo) Prepare(ctx context.Context) error {
	if r == nil || r.db == nil {
		return errors.New("oneapi repository is not configured")
	}
	if err := r.validateAuthoritativeSchema(); err != nil {
		return errors.Wrap(err, "validate oneapi schema")
	}
	if err := r.db.WithContext(ctx).AutoMigrate(
		&SSOUserLink{},
		&SSOOIDCIdentity{},
		&SSOEmailVerificationCode{},
		&SSOTOTPEnrollment{},
	); err != nil {
		return errors.Wrap(err, "migrate sso-owned oneapi tables")
	}
	return nil
}

func (r *Repo) validateAuthoritativeSchema() error {
	migrator := r.db.Migrator()
	required := []struct {
		model   any
		columns []string
	}{
		{model: &User{}, columns: []string{"id", columnUUID, "username", "password", "display_name", "role", "status", "email", "github_id", "totp_secret", "created_at", "updated_at"}},
		{model: &oneAPIToken{}, columns: []string{"id", columnUUID, "user_id", "user_uuid", "key", "status", "created_at", "updated_at"}},
		{model: &oneAPIOption{}, columns: []string{"key", "value"}},
		{model: &PasskeyCredential{}, columns: []string{"id", columnUUID, "user_id", "user_uuid", "credential_id", "public_key", "sign_count"}},
	}
	for _, requirement := range required {
		if !migrator.HasTable(requirement.model) {
			return errors.Errorf("required oneapi table for %T is missing", requirement.model)
		}
		for _, column := range requirement.columns {
			if !migrator.HasColumn(requirement.model, column) {
				return errors.Errorf("required oneapi column %T.%s is missing", requirement.model, column)
			}
		}
	}
	return nil
}

func (r *Repo) toBlogUser(ctx context.Context, user *User) (*blogmodel.User, error) {
	if user == nil || user.ID <= 0 {
		return nil, errors.New("oneapi user is invalid")
	}
	link, err := r.ensureUserLink(ctx, user)
	if err != nil {
		return nil, errors.Wrap(err, "ensure sso user link")
	}
	objectID, err := primitive.ObjectIDFromHex(link.BlogObjectID)
	if err != nil {
		return nil, errors.Wrap(err, "parse linked blog object id")
	}

	result := &blogmodel.User{
		ID:             objectID,
		OneAPIID:       user.ID,
		OneAPIUsername: user.Username,
		Role:           user.Role,
		UID:            link.SSOUID,
		ModifiedAt:     time.UnixMilli(user.UpdatedAt).UTC(),
		Username:       strings.TrimSpace(user.DisplayName),
		Account:        strings.TrimSpace(user.Email),
		Password:       user.Password,
		TOTPSecret:     user.TOTPSecret,
		TOTPEnabled:    strings.TrimSpace(user.TOTPSecret) != "",
		Status:         statusToBlog(user.Status),
	}
	if result.Username == "" {
		result.Username = user.Username
	}
	if result.Account == "" {
		result.Account = user.Username
	}

	passkeys, err := r.loadPasskeys(ctx, user.ID)
	if err != nil {
		return nil, errors.Wrap(err, "load oneapi passkeys")
	}
	result.Passkeys = passkeys
	identities, err := r.loadOIDCIdentities(ctx, user)
	if err != nil {
		return nil, errors.Wrap(err, "load sso oidc identities")
	}
	result.OIDCIdentities = identities
	return result, nil
}

func (r *Repo) ensureUserLink(ctx context.Context, user *User) (*SSOUserLink, error) {
	var link SSOUserLink
	err := r.db.WithContext(ctx).Where("oneapi_user_id = ?", user.ID).First(&link).Error
	if err == nil {
		return &link, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.Wrap(err, "find sso user link")
	}

	userUUID := strings.TrimSpace(user.UUID)
	if _, parseErr := uuid.Parse(userUUID); parseErr != nil {
		userUUID = gutils.UUID7()
		update := r.db.WithContext(ctx).Model(&User{}).
			Where("id = ? AND (uuid IS NULL OR uuid = '')", user.ID).
			Update("uuid", userUUID)
		if update.Error != nil {
			return nil, errors.Wrap(update.Error, "backfill oneapi user uuid")
		}
		if update.RowsAffected == 0 {
			if err = r.db.WithContext(ctx).Model(&User{}).Select("uuid").Where("id = ?", user.ID).Take(user).Error; err != nil {
				return nil, errors.Wrap(err, "reload oneapi user uuid")
			}
			userUUID = strings.TrimSpace(user.UUID)
		}
	}

	now := time.Now().UTC()
	link = SSOUserLink{
		OneAPIUserID:   user.ID,
		OneAPIUserUUID: userUUID,
		SSOUID:         userUUID,
		BlogObjectID:   blogmodel.SyntheticObjectID(user.ID).Hex(),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err = r.db.WithContext(ctx).Create(&link).Error; err != nil {
		if !errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, errors.Wrap(err, "create sso user link")
		}
		if err = r.db.WithContext(ctx).Where("oneapi_user_id = ?", user.ID).First(&link).Error; err != nil {
			return nil, errors.Wrap(err, "reload concurrently created sso user link")
		}
	}
	return &link, nil
}

func statusToBlog(status int) blogmodel.UserStatus {
	if status == StatusEnabled {
		return blogmodel.UserStatusActive
	}
	return blogmodel.UserStatusPending
}

func (r *Repo) loadPasskeys(ctx context.Context, userID int) ([]blogmodel.PasskeyCredential, error) {
	var rows []PasskeyCredential
	if err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at ASC").Find(&rows).Error; err != nil {
		return nil, errors.Wrap(err, "query oneapi passkeys")
	}
	result := make([]blogmodel.PasskeyCredential, 0, len(rows))
	for _, row := range rows {
		result = append(result, blogmodel.PasskeyCredential{
			ID:              base64.RawURLEncoding.EncodeToString(row.CredentialID),
			Name:            row.CredentialName,
			PublicKey:       base64.RawURLEncoding.EncodeToString(row.PublicKey),
			SignCount:       row.SignCount,
			CreatedAt:       time.UnixMilli(row.CreatedAt).UTC(),
			AttestationType: row.AttestationType,
			AAGUID:          base64.RawURLEncoding.EncodeToString(row.AAGUID),
			BackupEligible:  row.BackupEligible,
			BackupState:     row.BackupState,
			Transport:       row.Transport,
		})
	}
	return result, nil
}

func (r *Repo) loadOIDCIdentities(ctx context.Context, user *User) ([]blogmodel.OIDCIdentity, error) {
	var rows []SSOOIDCIdentity
	if err := r.db.WithContext(ctx).Where("user_id = ?", user.ID).Order("bound_at ASC").Find(&rows).Error; err != nil {
		return nil, errors.Wrap(err, "query sso oidc identities")
	}
	if len(rows) == 0 && strings.TrimSpace(user.GitHubID) != "" {
		rows = append(rows, SSOOIDCIdentity{
			Provider: oidcProviderGitHub,
			Subject:  user.GitHubID,
			UserID:   user.ID,
			Email:    user.Email,
			BoundAt:  time.UnixMilli(user.UpdatedAt).UTC(),
		})
	}
	result := make([]blogmodel.OIDCIdentity, 0, len(rows))
	for _, row := range rows {
		result = append(result, blogmodel.OIDCIdentity{
			Provider: row.Provider,
			Subject:  row.Subject,
			Email:    row.Email,
			BoundAt:  row.BoundAt.UTC(),
		})
	}
	return result, nil
}
