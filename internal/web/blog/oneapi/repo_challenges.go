package oneapi

import (
	"context"
	"time"

	"github.com/Laisky/errors/v2"
	"gorm.io/gorm"

	blogmodel "github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

// ReplaceEmailCode atomically replaces the active challenge for an
// account-purpose pair.
func (r *Repo) ReplaceEmailCode(ctx context.Context, code SSOEmailVerificationCode) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if deleteErr := tx.Where("account = ? AND purpose = ?", code.Account, code.Purpose).
			Delete(&SSOEmailVerificationCode{}).Error; deleteErr != nil {
			return errors.Wrap(deleteErr, "delete previous sso email code")
		}
		if createErr := tx.Create(&code).Error; createErr != nil {
			return errors.Wrap(createErr, "create sso email code")
		}
		return nil
	})
	if err != nil {
		return errors.WithStack(err)
	}
	return nil
}

// FindValidEmailCode returns the newest unexpired challenge for an
// account-purpose pair.
func (r *Repo) FindValidEmailCode(ctx context.Context, account string, purpose string, now time.Time) (*SSOEmailVerificationCode, error) {
	var code SSOEmailVerificationCode
	err := r.db.WithContext(ctx).
		Where("account = ? AND purpose = ? AND expires_at > ?", account, purpose, now.UTC()).
		Order("created_at DESC").First(&code).Error
	if err != nil {
		return nil, mapNotFound(err, "find valid sso email code")
	}
	return &code, nil
}

// ConsumeEmailCode deletes a challenge only when both its identifier and hash
// still match, making concurrent replay attempts single-use.
func (r *Repo) ConsumeEmailCode(ctx context.Context, id string, codeHash string) error {
	result := r.db.WithContext(ctx).
		Where("id = ? AND code_hash = ?", id, codeHash).
		Delete(&SSOEmailVerificationCode{})
	if result.Error != nil {
		return errors.Wrap(result.Error, "consume sso email code")
	}
	if result.RowsAffected != 1 {
		return errors.WithStack(ErrNotFound)
	}
	return nil
}

// DeleteEmailCode removes one challenge, for example when SMTP delivery fails.
func (r *Repo) DeleteEmailCode(ctx context.Context, id string) error {
	if err := r.db.WithContext(ctx).Where("id = ?", id).Delete(&SSOEmailVerificationCode{}).Error; err != nil {
		return errors.Wrap(err, "delete sso email code")
	}
	return nil
}

// UpsertTOTPEnrollment replaces a user's pending TOTP enrollment.
func (r *Repo) UpsertTOTPEnrollment(ctx context.Context, userID int, secret string, expiresAt time.Time) error {
	now := time.Now().UTC()
	enrollment := SSOTOTPEnrollment{UserID: userID, Secret: secret, CreatedAt: now, ExpiresAt: expiresAt.UTC()}
	if err := r.db.WithContext(ctx).Save(&enrollment).Error; err != nil {
		return errors.Wrap(err, "save pending totp enrollment")
	}
	return nil
}

// GetTOTPEnrollment returns an unexpired pending enrollment.
func (r *Repo) GetTOTPEnrollment(ctx context.Context, userID int, now time.Time) (*SSOTOTPEnrollment, error) {
	var enrollment SSOTOTPEnrollment
	err := r.db.WithContext(ctx).Where("user_id = ? AND expires_at > ?", userID, now.UTC()).First(&enrollment).Error
	if err != nil {
		return nil, mapNotFound(err, "find pending totp enrollment")
	}
	return &enrollment, nil
}

// ConfirmTOTPEnrollment atomically moves a pending secret to OneAPI's user
// record and deletes the pending enrollment.
func (r *Repo) ConfirmTOTPEnrollment(ctx context.Context, userID int, secret string) (*blogmodel.User, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		consume := tx.Where("user_id = ? AND secret = ? AND expires_at > ?", userID, secret, time.Now().UTC()).
			Delete(&SSOTOTPEnrollment{})
		if consume.Error != nil {
			return errors.Wrap(consume.Error, "consume confirmed totp enrollment")
		}
		if consume.RowsAffected != 1 {
			return errors.WithStack(ErrNotFound)
		}
		update := tx.Model(&User{}).Where("id = ?", userID).Update("totp_secret", secret)
		if update.Error != nil {
			return errors.Wrap(update.Error, "set oneapi totp secret")
		}
		if update.RowsAffected == 0 {
			return errors.WithStack(ErrNotFound)
		}
		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return r.GetByID(ctx, userID)
}

// ClearTOTP removes a OneAPI user's confirmed TOTP secret and pending setup.
func (r *Repo) ClearTOTP(ctx context.Context, userID int) (*blogmodel.User, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		update := tx.Model(&User{}).Where("id = ?", userID).Update("totp_secret", "")
		if update.Error != nil {
			return errors.Wrap(update.Error, "clear oneapi totp secret")
		}
		if update.RowsAffected == 0 {
			return errors.WithStack(ErrNotFound)
		}
		if deleteErr := tx.Where("user_id = ?", userID).Delete(&SSOTOTPEnrollment{}).Error; deleteErr != nil {
			return errors.Wrap(deleteErr, "delete pending totp enrollment")
		}
		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return r.GetByID(ctx, userID)
}

// ImportConfirmedTOTP stores a previously confirmed TOTP secret during an
// explicit migration. Pending legacy enrollments must not call this method.
func (r *Repo) ImportConfirmedTOTP(ctx context.Context, userID int, secret string) error {
	if userID <= 0 || secret == "" || len(secret) > 64 {
		return errors.New("confirmed totp migration input is invalid")
	}
	result := r.db.WithContext(ctx).Model(&User{}).Where("id = ?", userID).Update("totp_secret", secret)
	if result.Error != nil {
		return errors.Wrap(result.Error, "import confirmed oneapi totp secret")
	}
	if result.RowsAffected == 0 {
		return errors.WithStack(ErrNotFound)
	}
	return nil
}
