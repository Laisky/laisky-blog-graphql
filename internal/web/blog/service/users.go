package service

import (
	"context"
	"strings"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	gcrypto "github.com/Laisky/go-utils/v6/crypto"
	"github.com/Laisky/zap"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

// ValidateTOTPCode validates a one-time code for a user that has TOTP enabled.
// It accepts a user and raw TOTP code, returning nil when TOTP is disabled or the code is valid.
func (s *Blog) ValidateTOTPCode(_ context.Context, user *model.User, code string) error {
	if user == nil {
		return errors.New("user is nil")
	}
	if !user.TOTPEnabled {
		return nil
	}

	if !verifyTOTPCode(user.TOTPSecret, code) {
		return errors.New("invalid totp code")
	}

	return nil
}

// ValidateLogin validates an account and password pair.
// It accepts a context, account, and password, returning the matching user on success.
func (s *Blog) ValidateLogin(ctx context.Context, account, password string) (u *model.User, err error) {
	if account, err = sanitizeUserAccount(account); err != nil {
		return nil, errors.Wrap(err, "sanitize account")
	}
	if password, err = sanitizeUserPassword(password); err != nil {
		return nil, errors.Wrap(err, "sanitize password")
	}

	return s.dao.ValidateLogin(ctx, account, password)
}

// ChangePassword updates the authenticated user's password after validating the old password.
// It accepts a context, user, current password, and new password, returning the updated user.
func (s *Blog) ChangePassword(ctx context.Context, user *model.User, currentPassword string, newPassword string) (*model.User, error) {
	if user == nil {
		return nil, errors.New("user is nil")
	}

	currentPassword, err := sanitizeUserPassword(currentPassword)
	if err != nil {
		return nil, errors.Wrap(err, "sanitize current password")
	}
	newPassword, err = sanitizeUserPassword(newPassword)
	if err != nil {
		return nil, errors.Wrap(err, "sanitize new password")
	}
	if strings.TrimSpace(currentPassword) == strings.TrimSpace(newPassword) {
		return nil, errors.New("new password must be different from current password")
	}

	if err = gcrypto.VerifyHashedPassword([]byte(currentPassword), user.Password); err != nil {
		return nil, errors.WithStack(model.ErrInvalidCredentials)
	}

	hashed, err := gcrypto.PasswordHash([]byte(newPassword), gutils.HashTypeSha256)
	if err != nil {
		return nil, errors.Wrap(err, "hash new password")
	}

	now := gutils.Clock.GetUTCNow()
	col := s.dao.GetUsersCol()
	if _, err = col.UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{
		"$set": bson.M{
			"password":          hashed,
			"post_modified_gmt": now,
		},
	}); err != nil {
		return nil, errors.Wrapf(err, "update password for user %s", user.ID.Hex())
	}

	user.Password = hashed
	user.ModifiedAt = now
	return user, nil
}

// StartTOTPSetup creates a pending TOTP secret for the authenticated user.
// It accepts a context and user, returning the secret and provisioning URI.
func (s *Blog) StartTOTPSetup(ctx context.Context, user *model.User) (string, string, error) {
	if user == nil {
		return "", "", errors.New("user is nil")
	}

	secret := newTOTPSecret()
	if secret == "" {
		return "", "", errors.New("generate totp secret")
	}

	now := gutils.Clock.GetUTCNow()
	col := s.dao.GetUsersCol()
	if _, err := col.UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{
		"$set": bson.M{
			"totp_secret":       secret,
			"totp_enabled":      false,
			"post_modified_gmt": now,
		},
	}); err != nil {
		return "", "", errors.Wrapf(err, "store totp secret for user %s", user.ID.Hex())
	}

	user.TOTPSecret = secret
	user.TOTPEnabled = false
	user.ModifiedAt = now
	return secret, buildTOTPProvisioningURI(user.Account, secret), nil
}

// ConfirmTOTPSetup verifies the current TOTP code and enables TOTP for the user.
// It accepts a context, user, and code, returning the updated user.
func (s *Blog) ConfirmTOTPSetup(ctx context.Context, user *model.User, code string) (*model.User, error) {
	if user == nil {
		return nil, errors.New("user is nil")
	}
	if user.TOTPSecret == "" {
		return nil, errors.New("totp setup has not been started")
	}
	if !verifyTOTPCode(user.TOTPSecret, code) {
		return nil, errors.New("invalid totp code")
	}

	now := gutils.Clock.GetUTCNow()
	col := s.dao.GetUsersCol()
	if _, err := col.UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{
		"$set": bson.M{
			"totp_enabled":      true,
			"post_modified_gmt": now,
		},
	}); err != nil {
		return nil, errors.Wrapf(err, "enable totp for user %s", user.ID.Hex())
	}

	user.TOTPEnabled = true
	user.ModifiedAt = now
	return user, nil
}

// DisableTOTP disables TOTP for the authenticated user after verifying the current password.
// It accepts a context, user, and current password, returning the updated user.
func (s *Blog) DisableTOTP(ctx context.Context, user *model.User, currentPassword string) (*model.User, error) {
	if user == nil {
		return nil, errors.New("user is nil")
	}
	currentPassword, err := sanitizeUserPassword(currentPassword)
	if err != nil {
		return nil, errors.Wrap(err, "sanitize current password")
	}
	if err = gcrypto.VerifyHashedPassword([]byte(currentPassword), user.Password); err != nil {
		return nil, errors.WithStack(model.ErrInvalidCredentials)
	}

	now := gutils.Clock.GetUTCNow()
	col := s.dao.GetUsersCol()
	if _, err = col.UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{
		"$set": bson.M{
			"totp_secret":       "",
			"totp_enabled":      false,
			"post_modified_gmt": now,
		},
	}); err != nil {
		return nil, errors.Wrapf(err, "disable totp for user %s", user.ID.Hex())
	}

	user.TOTPSecret = ""
	user.TOTPEnabled = false
	user.ModifiedAt = now
	return user, nil
}

func (s *Blog) setupUserCols(ctx context.Context) error {
	col := s.dao.GetUsersCol()

	// create unique index for account
	{
		if _, err := col.Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys: bson.M{
				"account": 1,
			},
			Options: options.Index().SetUnique(true),
		}); err != nil {
			return errors.Wrap(err, "create index for account")
		}
	}

	// create unique sparse index for external user uid
	{
		if _, err := col.Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys: bson.M{
				"uid": 1,
			},
			Options: options.Index().SetUnique(true).SetSparse(true),
		}); err != nil {
			return errors.Wrap(err, "create index for uid")
		}
	}

	// create index for external identity lookups
	{
		if _, err := col.Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys: bson.D{
				{Key: "oidc_identities.provider", Value: 1},
				{Key: "oidc_identities.subject", Value: 1},
			},
		}); err != nil {
			return errors.Wrap(err, "create index for oidc identity")
		}
	}

	// create index for passkey credential lookups
	{
		if _, err := col.Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys: bson.D{
				{Key: "passkeys.id", Value: 1},
			},
		}); err != nil {
			return errors.Wrap(err, "create index for passkey credential")
		}
	}

	if err := s.setupEmailVerificationCols(ctx); err != nil {
		return errors.Wrap(err, "setup email verification cols")
	}

	return nil
}

// UserRegister creates an active email/password user after verifying the email code.
// It accepts account credentials, display name, and email code, returning the created user.
func (s *Blog) UserRegister(ctx context.Context,
	account, password, displayName string, emailCode string) (u *model.User, err error) {
	if account, err = sanitizeUserAccount(account); err != nil {
		return nil, errors.Wrap(err, "sanitize account")
	}
	if password, err = sanitizeUserPassword(password); err != nil {
		return nil, errors.Wrap(err, "sanitize password")
	}
	if displayName, err = sanitizeUserDisplayName(displayName); err != nil {
		return nil, errors.Wrap(err, "sanitize display name")
	}
	if err = s.ConsumeEmailVerificationCode(ctx, account, model.EmailVerificationPurposeRegister, emailCode); err != nil {
		return nil, errors.Wrap(err, "verify email code")
	}

	col := s.dao.GetUsersCol()
	user := model.NewUser()
	user.Account = account
	user.Username = displayName
	user.Status = model.UserStatusActive
	user.ActiveToken = ""

	// check duplicate
	existedUser := new(model.User)
	err = col.FindOne(ctx, bson.M{"account": account}).Decode(existedUser)
	if err != nil {
		if !errors.Is(err, mongo.ErrNoDocuments) {
			return nil, errors.Wrapf(err, "find user %q", account)
		}
	} else {
		return nil, errors.New("account already exists")
	}

	pwd, err := gcrypto.PasswordHash([]byte(password), gutils.HashTypeSha256)
	if err != nil {
		return nil, errors.Wrapf(err, "generate password hash for %q", account)
	}
	user.Password = pwd

	// insert new user
	if _, err = col.InsertOne(ctx, user); err != nil {
		return nil, errors.Wrapf(err, "insert user %q", account)
	}

	s.logger.Info("insert new user", zap.String("account", account))
	return user, nil
}

func (s *Blog) UserActive(ctx context.Context, account, activeToken string) (u *model.User, err error) {
	col := s.dao.GetUsersCol()

	if account, err = sanitizeUserAccount(account); err != nil {
		return nil, errors.Wrap(err, "sanitize account")
	}
	if activeToken, err = sanitizeActiveToken(activeToken); err != nil {
		return nil, errors.Wrap(err, "sanitize active token")
	}

	user := new(model.User)
	if err = col.FindOne(ctx, bson.M{"account": account}).Decode(user); err != nil {
		return nil, errors.Wrapf(err, "find user %q", account)
	}

	if !secureCompareString(user.ActiveToken, activeToken) {
		return nil, errors.New("invalid active token")
	}

	user.ModifiedAt = gutils.Clock.GetUTCNow()
	user.Status = model.UserStatusActive
	user.ActiveToken = ""

	// save user
	if _, err = col.UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{"$set": user}); err != nil {
		return nil, errors.Wrapf(err, "update user %q", account)
	}

	return user, nil
}
