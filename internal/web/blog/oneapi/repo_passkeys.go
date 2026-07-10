package oneapi

import (
	"context"
	"encoding/base64"
	"strings"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/go-webauthn/webauthn/webauthn"
	"gorm.io/gorm"

	blogmodel "github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

// FindByPasskeyCredentialID loads the enabled owner of a raw WebAuthn
// credential identifier.
func (r *Repo) FindByPasskeyCredentialID(ctx context.Context, credentialID []byte) (*blogmodel.User, error) {
	if len(credentialID) == 0 || len(credentialID) > 1024 {
		return nil, errors.New("passkey credential id is invalid")
	}
	var passkey PasskeyCredential
	if err := r.db.WithContext(ctx).Where("credential_id = ?", credentialID).First(&passkey).Error; err != nil {
		return nil, mapNotFound(err, "find oneapi passkey")
	}
	user, err := r.GetByID(ctx, passkey.UserID)
	if err != nil {
		return nil, err
	}
	if user.Status != blogmodel.UserStatusActive {
		return nil, errors.WithStack(blogmodel.ErrInvalidCredentials)
	}
	return user, nil
}

// AddPasskey stores a verified credential in OneAPI's native passkey table.
func (r *Repo) AddPasskey(ctx context.Context, user *blogmodel.User, name string, credential *webauthn.Credential) (*blogmodel.User, error) {
	if user == nil || user.OneAPIID <= 0 || credential == nil {
		return nil, errors.New("oneapi user and passkey credential are required")
	}
	if len(credential.ID) == 0 || len(credential.ID) > 1024 || len(credential.PublicKey) == 0 || len(credential.PublicKey) > 1024 {
		return nil, errors.New("passkey credential material is invalid")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Passkey"
	}
	if len(name) > 128 {
		return nil, errors.New("passkey name exceeds 128 bytes")
	}
	userUUID := user.UID
	row := PasskeyCredential{
		UUID:            gutils.UUID7(),
		UserID:          user.OneAPIID,
		UserUUID:        &userUUID,
		CredentialName:  name,
		CredentialID:    credential.ID,
		PublicKey:       credential.PublicKey,
		AttestationType: credential.AttestationType,
		AAGUID:          credential.Authenticator.AAGUID,
		SignCount:       credential.Authenticator.SignCount,
		BackupEligible:  credential.Flags.BackupEligible,
		BackupState:     credential.Flags.BackupState,
		Transport:       transportsToString(credential),
	}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, errors.New("passkey already exists")
		}
		return nil, errors.Wrap(err, "create oneapi passkey")
	}
	return r.GetByID(ctx, user.OneAPIID)
}

// ImportPasskey idempotently imports a legacy credential and rejects an
// existing credential owned by another user.
func (r *Repo) ImportPasskey(ctx context.Context, user *blogmodel.User, name string, credential *webauthn.Credential) (*blogmodel.User, error) {
	if user == nil || credential == nil || len(credential.ID) == 0 {
		return nil, errors.New("legacy passkey import input is invalid")
	}
	var existing PasskeyCredential
	err := r.db.WithContext(ctx).Where("credential_id = ?", credential.ID).First(&existing).Error
	if err == nil {
		if existing.UserID != user.OneAPIID {
			return nil, errors.New("passkey credential is already owned by another user")
		}
		return r.GetByID(ctx, user.OneAPIID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.Wrap(err, "check legacy passkey owner")
	}
	return r.AddPasskey(ctx, user, name, credential)
}

// UpdatePasskey updates the credential state after successful authentication.
func (r *Repo) UpdatePasskey(ctx context.Context, userID int, credential *webauthn.Credential) (*blogmodel.User, error) {
	if userID <= 0 || credential == nil || len(credential.ID) == 0 {
		return nil, errors.New("oneapi user and passkey credential are required")
	}
	result := r.db.WithContext(ctx).Model(&PasskeyCredential{}).
		Where("user_id = ? AND credential_id = ?", userID, credential.ID).
		Updates(map[string]any{
			"public_key":       credential.PublicKey,
			"attestation_type": credential.AttestationType,
			"aaguid":           credential.Authenticator.AAGUID,
			"sign_count":       credential.Authenticator.SignCount,
			"backup_eligible":  credential.Flags.BackupEligible,
			"backup_state":     credential.Flags.BackupState,
			"transport":        transportsToString(credential),
		})
	if result.Error != nil {
		return nil, errors.Wrap(result.Error, "update oneapi passkey")
	}
	if result.RowsAffected == 0 {
		return nil, errors.WithStack(ErrNotFound)
	}
	return r.GetByID(ctx, userID)
}

// RenamePasskey changes a passkey's user-visible name after enforcing
// ownership.
func (r *Repo) RenamePasskey(ctx context.Context, userID int, encodedCredentialID string, name string) (*blogmodel.User, error) {
	credentialID, err := decodeCredentialID(encodedCredentialID)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return nil, errors.New("passkey name is invalid")
	}
	result := r.db.WithContext(ctx).Model(&PasskeyCredential{}).
		Where("user_id = ? AND credential_id = ?", userID, credentialID).
		Update("credential_name", name)
	if result.Error != nil {
		return nil, errors.Wrap(result.Error, "rename oneapi passkey")
	}
	if result.RowsAffected == 0 {
		return nil, errors.WithStack(ErrNotFound)
	}
	return r.GetByID(ctx, userID)
}

// DeletePasskey removes an owned passkey credential.
func (r *Repo) DeletePasskey(ctx context.Context, userID int, encodedCredentialID string) (*blogmodel.User, error) {
	credentialID, err := decodeCredentialID(encodedCredentialID)
	if err != nil {
		return nil, err
	}
	result := r.db.WithContext(ctx).
		Where("user_id = ? AND credential_id = ?", userID, credentialID).
		Delete(&PasskeyCredential{})
	if result.Error != nil {
		return nil, errors.Wrap(result.Error, "delete oneapi passkey")
	}
	if result.RowsAffected == 0 {
		return nil, errors.WithStack(ErrNotFound)
	}
	return r.GetByID(ctx, userID)
}

func decodeCredentialID(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" || len(encoded) > 2048 {
		return nil, errors.New("passkey credential id is invalid")
	}
	credentialID, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.Wrap(err, "decode passkey credential id")
	}
	if len(credentialID) == 0 || len(credentialID) > 1024 {
		return nil, errors.New("decoded passkey credential id is invalid")
	}
	return credentialID, nil
}

func transportsToString(credential *webauthn.Credential) string {
	values := make([]string, 0, len(credential.Transport))
	for _, transport := range credential.Transport {
		values = append(values, string(transport))
	}
	return strings.Join(values, ",")
}
