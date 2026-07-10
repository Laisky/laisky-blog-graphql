package oneapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/Laisky/zap"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"gorm.io/gorm"

	blogmodel "github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

// AccountIdentity is the minimal authoritative OneAPI identity needed by the
// legacy SSO migration preflight.
type AccountIdentity struct {
	UserID       int
	UserUUID     string
	HasPassword  bool
	AccountEmail string
}

// LookupAccountIdentity finds a OneAPI account without creating an SSO link.
func (r *Repo) LookupAccountIdentity(ctx context.Context, account string) (*AccountIdentity, error) {
	var user User
	if err := r.db.WithContext(ctx).Where("email = ?", strings.TrimSpace(account)).First(&user).Error; err != nil {
		return nil, mapNotFound(err, "lookup oneapi account identity")
	}
	return &AccountIdentity{UserID: user.ID, UserUUID: user.UUID, HasPassword: user.Password != "", AccountEmail: user.Email}, nil
}

// ImportLegacyUserLink preserves an existing SSO UUID and Mongo blog author
// ObjectID for a matched OneAPI user. It is idempotent and refuses to overwrite
// a conflicting mapping.
func (r *Repo) ImportLegacyUserLink(ctx context.Context, oneAPIUserID int, ssoUID string, blogObjectID primitive.ObjectID) error {
	if oneAPIUserID <= 0 || blogObjectID.IsZero() {
		return errors.New("legacy user link identifiers are invalid")
	}
	ssoUID = strings.TrimSpace(ssoUID)
	if _, err := uuid.Parse(ssoUID); err != nil {
		return errors.Wrap(err, "parse legacy sso uid")
	}
	var user User
	if err := r.db.WithContext(ctx).Where("id = ?", oneAPIUserID).First(&user).Error; err != nil {
		return mapNotFound(err, "find oneapi user for legacy link")
	}
	now := time.Now().UTC()
	desired := SSOUserLink{OneAPIUserID: user.ID, OneAPIUserUUID: user.UUID, SSOUID: ssoUID,
		BlogObjectID: blogObjectID.Hex(), CreatedAt: now, UpdatedAt: now}
	var existing SSOUserLink
	err := r.db.WithContext(ctx).Where("oneapi_user_id = ?", oneAPIUserID).First(&existing).Error
	if err == nil {
		if existing.OneAPIUserUUID != desired.OneAPIUserUUID || existing.SSOUID != desired.SSOUID ||
			existing.BlogObjectID != desired.BlogObjectID {
			return errors.New("oneapi user already has a conflicting sso identity link")
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return errors.Wrap(err, "find existing legacy sso user link")
	}
	if err = r.db.WithContext(ctx).Create(&desired).Error; err != nil {
		return errors.Wrap(err, "create legacy sso user link")
	}
	return nil
}

// GetByID loads a OneAPI user by its internal numeric identifier.
func (r *Repo) GetByID(ctx context.Context, id int) (*blogmodel.User, error) {
	if id <= 0 {
		return nil, errors.New("oneapi user id is invalid")
	}
	var user User
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&user).Error; err != nil {
		return nil, mapNotFound(err, "get oneapi user by id")
	}
	return r.toBlogUser(ctx, &user)
}

// GetByBlogObjectID resolves a stable blog author ObjectID to its OneAPI user.
func (r *Repo) GetByBlogObjectID(ctx context.Context, objectID primitive.ObjectID) (*blogmodel.User, error) {
	if objectID.IsZero() {
		return nil, errors.New("blog object id is empty")
	}
	var link SSOUserLink
	err := r.db.WithContext(ctx).Where("blog_object_id = ?", objectID.Hex()).First(&link).Error
	if err == nil {
		return r.GetByID(ctx, link.OneAPIUserID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.Wrap(err, "find sso user by blog object id")
	}
	if oneAPIID, ok := blogmodel.OneAPIIDFromSyntheticObjectID(objectID); ok {
		return r.GetByID(ctx, oneAPIID)
	}
	return nil, errors.WithStack(ErrNotFound)
}

// GetByUID resolves the public SSO UUID to its authoritative OneAPI user.
func (r *Repo) GetByUID(ctx context.Context, uid string) (*blogmodel.User, error) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return nil, errors.New("sso uid is empty")
	}
	var link SSOUserLink
	err := r.db.WithContext(ctx).Where("sso_uid = ?", uid).First(&link).Error
	if err == nil {
		return r.GetByID(ctx, link.OneAPIUserID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.Wrap(err, "find sso user link by uid")
	}

	var user User
	if err = r.db.WithContext(ctx).Where("uuid = ?", uid).First(&user).Error; err != nil {
		return nil, mapNotFound(err, "find oneapi user by uuid")
	}
	return r.toBlogUser(ctx, &user)
}

// FindByAccount loads a OneAPI user by normalized email address.
func (r *Repo) FindByAccount(ctx context.Context, account string) (*blogmodel.User, error) {
	var user User
	if err := r.db.WithContext(ctx).Where("email = ?", strings.TrimSpace(account)).First(&user).Error; err != nil {
		return nil, mapNotFound(err, "find oneapi user by email")
	}
	return r.toBlogUser(ctx, &user)
}

// FindByLogin applies OneAPI's username-before-email lookup rule.
func (r *Repo) FindByLogin(ctx context.Context, login string) (*blogmodel.User, error) {
	user, err := r.findRawByLogin(ctx, login)
	if err != nil {
		return nil, err
	}
	return r.toBlogUser(ctx, user)
}

func (r *Repo) findRawByLogin(ctx context.Context, login string) (*User, error) {
	login = strings.TrimSpace(login)
	if login == "" {
		return nil, errors.WithStack(ErrNotFound)
	}
	var user User
	err := r.db.WithContext(ctx).Where("username = ?", login).First(&user).Error
	if err == nil {
		return &user, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.Wrap(err, "find oneapi user by username")
	}
	if err = r.db.WithContext(ctx).Where("email = ?", login).First(&user).Error; err != nil {
		return nil, mapNotFound(err, "find oneapi user by email login")
	}
	return &user, nil
}

// ValidateLogin authenticates a OneAPI username-or-email and bcrypt password
// without revealing whether the account exists.
func (r *Repo) ValidateLogin(ctx context.Context, login string, password string) (*blogmodel.User, error) {
	user, err := r.findRawByLogin(ctx, login)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			_ = ValidatePasswordAndHash(password, dummyPasswordHash())
			return nil, errors.WithStack(blogmodel.ErrInvalidCredentials)
		}
		return nil, err
	}
	if user.Password == "" || !ValidatePasswordAndHash(password, user.Password) || user.Status != StatusEnabled {
		return nil, errors.WithStack(blogmodel.ErrInvalidCredentials)
	}
	return r.toBlogUser(ctx, user)
}

// VerifyPassword checks a plaintext password against a hydrated OneAPI user.
func (r *Repo) VerifyPassword(user *blogmodel.User, password string) bool {
	return user != nil && user.Password != "" && ValidatePasswordAndHash(password, user.Password)
}

// ChangePassword replaces a OneAPI user's password with a compatible bcrypt
// hash and returns the refreshed SSO model.
func (r *Repo) ChangePassword(ctx context.Context, userID int, password string) (*blogmodel.User, error) {
	hash, err := Password2Hash(password)
	if err != nil {
		return nil, errors.Wrap(err, "hash oneapi password")
	}
	result := r.db.WithContext(ctx).Model(&User{}).Where("id = ?", userID).Update("password", hash)
	if result.Error != nil {
		return nil, errors.Wrap(result.Error, "update oneapi password")
	}
	if result.RowsAffected == 0 {
		return nil, errors.WithStack(ErrNotFound)
	}
	return r.GetByID(ctx, userID)
}

// CreateUser creates an enabled OneAPI email/password user, its stable SSO
// identity link, and a best-effort default OneAPI token.
func (r *Repo) CreateUser(ctx context.Context, email string, password string, displayName string) (*blogmodel.User, error) {
	user, err := r.newOneAPIUser(ctx, email, password, displayName)
	if err != nil {
		return nil, err
	}
	if err = r.createUserAndLink(ctx, user); err != nil {
		return nil, err
	}
	if err = r.createDefaultToken(ctx, user); err != nil {
		r.logger.Warn("create default token for sso user", zap.Int("user_id", user.ID), zap.Error(err))
	}
	return r.toBlogUser(ctx, user)
}

// RegisterUserWithEmailCode atomically consumes a verified email challenge and
// creates the corresponding OneAPI user and stable SSO link.
func (r *Repo) RegisterUserWithEmailCode(ctx context.Context, codeID string, codeHash string,
	email string, password string, displayName string) (*blogmodel.User, error) {
	user, err := r.newOneAPIUser(ctx, email, password, displayName)
	if err != nil {
		return nil, err
	}
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		consume := tx.Where("id = ? AND code_hash = ?", codeID, codeHash).Delete(&SSOEmailVerificationCode{})
		if consume.Error != nil {
			return errors.Wrap(consume.Error, "consume registration email code")
		}
		if consume.RowsAffected != 1 {
			return errors.WithStack(blogmodel.ErrInvalidCredentials)
		}
		return r.createUserAndLinkTx(tx, user)
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if err = r.createDefaultToken(ctx, user); err != nil {
		r.logger.Warn("create default token for registered sso user", zap.Int("user_id", user.ID), zap.Error(err))
	}
	return r.toBlogUser(ctx, user)
}

func (r *Repo) newOneAPIUser(ctx context.Context, email string, password string, displayName string) (*User, error) {
	hash, err := Password2Hash(password)
	if err != nil {
		return nil, errors.Wrap(err, "hash oneapi user password")
	}
	quota, err := r.newUserQuota(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "load oneapi new-user quota")
	}
	affCode, err := r.generateUniqueAffCode(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "generate oneapi affiliation code")
	}

	return &User{
		UUID:        gutils.UUID7(),
		Username:    usernameForEmail(email),
		Password:    hash,
		DisplayName: displayName,
		Role:        RoleCommonUser,
		Status:      StatusEnabled,
		Email:       email,
		AccessToken: strings.ReplaceAll(gutils.UUID7(), "-", ""),
		AffCode:     affCode,
		Quota:       quota,
	}, nil
}

func (r *Repo) createUserAndLink(ctx context.Context, user *User) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return r.createUserAndLinkTx(tx, user)
	})
	if err != nil {
		return errors.WithStack(err)
	}
	return nil
}

func (r *Repo) createUserAndLinkTx(tx *gorm.DB, user *User) error {
	var count int64
	if err := tx.Model(&User{}).Where("email = ? OR username = ?", user.Email, user.Username).Count(&count).Error; err != nil {
		return errors.Wrap(err, "check existing oneapi account")
	}
	if count > 0 {
		return errors.WithStack(blogmodel.ErrAccountExists)
	}
	if err := tx.Create(user).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return errors.WithStack(blogmodel.ErrAccountExists)
		}
		return errors.Wrap(err, "create oneapi user")
	}
	now := time.Now().UTC()
	link := SSOUserLink{OneAPIUserID: user.ID, OneAPIUserUUID: user.UUID, SSOUID: user.UUID,
		BlogObjectID: blogmodel.SyntheticObjectID(user.ID).Hex(), CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&link).Error; err != nil {
		return errors.Wrap(err, "create sso user link")
	}
	return nil
}

func (r *Repo) createDefaultToken(ctx context.Context, user *User) error {
	now := time.Now().UTC()
	key, err := generateTokenKey()
	if err != nil {
		return errors.Wrap(err, "generate oneapi token key")
	}
	token := oneAPIToken{
		UUID:           gutils.UUID7(),
		UserID:         user.ID,
		UserUUID:       &user.UUID,
		Key:            key,
		Status:         tokenStatusEnabled,
		Name:           "default",
		CreatedTime:    now.Unix(),
		AccessedTime:   now.Unix(),
		ExpiredTime:    -1,
		RemainQuota:    -1,
		UnlimitedQuota: true,
	}
	if err = r.db.WithContext(ctx).Create(&token).Error; err != nil {
		return errors.Wrap(err, "insert default oneapi token")
	}
	return nil
}

func (r *Repo) newUserQuota(ctx context.Context) (int64, error) {
	var option oneAPIOption
	err := r.db.WithContext(ctx).Where("key = ?", "QuotaForNewUser").First(&option).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, errors.Wrap(err, "query QuotaForNewUser option")
	}
	quota, err := strconv.ParseInt(strings.TrimSpace(option.Value), 10, 64)
	if err != nil {
		return 0, errors.Wrap(err, "parse QuotaForNewUser option")
	}
	return quota, nil
}

func (r *Repo) generateUniqueAffCode(ctx context.Context) (string, error) {
	for range 16 {
		code, err := randomString(4)
		if err != nil {
			return "", err
		}
		var count int64
		if err = r.db.WithContext(ctx).Model(&User{}).Where("aff_code = ?", code).Count(&count).Error; err != nil {
			return "", errors.Wrap(err, "count oneapi affiliation code")
		}
		if count == 0 {
			return code, nil
		}
	}
	return "", errors.New("could not generate a unique oneapi affiliation code")
}

func usernameForEmail(email string) string {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if len(normalized) <= 30 {
		return normalized
	}
	digest := sha256.Sum256([]byte(normalized))
	return normalized[:17] + "-" + hex.EncodeToString(digest[:6])
}

func mapNotFound(err error, operation string) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errors.WithStack(ErrNotFound)
	}
	return errors.Wrap(err, operation)
}
