package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/go-webauthn/webauthn/webauthn"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

// FindUserByPasskeyID loads a user that owns the provided WebAuthn credential ID.
// It accepts a context and raw credential ID bytes, returning the matched user.
func (s *Blog) FindUserByPasskeyID(ctx context.Context, credentialID []byte) (*model.User, error) {
	encodedID := base64.RawURLEncoding.EncodeToString(credentialID)
	if encodedID == "" {
		return nil, errors.New("passkey credential id is empty")
	}

	user := new(model.User)
	if err := s.dao.GetUsersCol().FindOne(ctx, bson.M{"passkeys.id": encodedID}).Decode(user); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, errors.WithStack(mongo.ErrNoDocuments)
		}
		return nil, errors.Wrap(err, "find user by passkey id")
	}

	return user, nil
}

// AddPasskeyCredential stores a validated WebAuthn credential for a user.
// It accepts context, user, label, and credential, returning the updated user.
func (s *Blog) AddPasskeyCredential(ctx context.Context,
	user *model.User,
	label string,
	credential *webauthn.Credential,
) (*model.User, error) {
	if user == nil {
		return nil, errors.New("user is nil")
	}
	if credential == nil {
		return nil, errors.New("credential is nil")
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = "Passkey"
	}

	encodedID := base64.RawURLEncoding.EncodeToString(credential.ID)
	for _, passkey := range user.Passkeys {
		if passkey.ID == encodedID {
			return nil, errors.New("passkey already exists")
		}
	}

	credentialJSON, err := json.Marshal(credential)
	if err != nil {
		return nil, errors.Wrap(err, "marshal passkey credential")
	}

	now := gutils.Clock.GetUTCNow()
	passkey := model.PasskeyCredential{
		ID:             encodedID,
		Name:           label,
		PublicKey:      base64.RawURLEncoding.EncodeToString(credential.PublicKey),
		CredentialJSON: string(credentialJSON),
		SignCount:      credential.Authenticator.SignCount,
		CreatedAt:      now,
	}

	result, err := s.dao.GetUsersCol().UpdateOne(ctx, bson.M{
		"_id": user.ID,
		"passkeys": bson.M{
			"$not": bson.M{
				"$elemMatch": bson.M{"id": encodedID},
			},
		},
	}, bson.M{
		"$push": bson.M{"passkeys": passkey},
		"$set":  bson.M{"post_modified_gmt": now},
	})
	if err != nil {
		return nil, errors.Wrapf(err, "add passkey for user %s", user.ID.Hex())
	}
	if result.ModifiedCount == 0 {
		return nil, errors.New("passkey already exists")
	}

	user.Passkeys = append(user.Passkeys, passkey)
	user.ModifiedAt = now
	return user, nil
}

// UpdatePasskeyCredential stores the latest credential counter after a successful login.
// It accepts context, user, and credential, returning the updated user.
func (s *Blog) UpdatePasskeyCredential(ctx context.Context,
	user *model.User,
	credential *webauthn.Credential,
) (*model.User, error) {
	if user == nil {
		return nil, errors.New("user is nil")
	}
	if credential == nil {
		return nil, errors.New("credential is nil")
	}

	encodedID := base64.RawURLEncoding.EncodeToString(credential.ID)
	credentialJSON, err := json.Marshal(credential)
	if err != nil {
		return nil, errors.Wrap(err, "marshal passkey credential")
	}

	now := gutils.Clock.GetUTCNow()
	result, err := s.dao.GetUsersCol().UpdateOne(ctx, bson.M{
		"_id":         user.ID,
		"passkeys.id": encodedID,
	}, bson.M{
		"$set": bson.M{
			"passkeys.$.credential_json": string(credentialJSON),
			"passkeys.$.public_key":      base64.RawURLEncoding.EncodeToString(credential.PublicKey),
			"passkeys.$.sign_count":      credential.Authenticator.SignCount,
			"post_modified_gmt":          now,
		},
	})
	if err != nil {
		return nil, errors.Wrapf(err, "update passkey for user %s", user.ID.Hex())
	}
	if result.MatchedCount == 0 {
		return nil, errors.New("passkey credential not found")
	}

	for idx := range user.Passkeys {
		if user.Passkeys[idx].ID == encodedID {
			user.Passkeys[idx].CredentialJSON = string(credentialJSON)
			user.Passkeys[idx].PublicKey = base64.RawURLEncoding.EncodeToString(credential.PublicKey)
			user.Passkeys[idx].SignCount = credential.Authenticator.SignCount
			break
		}
	}
	user.ModifiedAt = now
	return user, nil
}

// RenamePasskeyCredential updates the user-visible label for one passkey.
// It accepts a context, user, credential ID, and new label, returning the updated user.
func (s *Blog) RenamePasskeyCredential(ctx context.Context,
	user *model.User,
	credentialID string,
	name string,
) (*model.User, error) {
	if user == nil {
		return nil, errors.New("user is nil")
	}
	credentialID = strings.TrimSpace(credentialID)
	if credentialID == "" {
		return nil, errors.New("passkey credential id is empty")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("passkey name is empty")
	}

	now := gutils.Clock.GetUTCNow()
	result, err := s.dao.GetUsersCol().UpdateOne(ctx, bson.M{
		"_id":         user.ID,
		"passkeys.id": credentialID,
	}, bson.M{
		"$set": bson.M{
			"passkeys.$.name":   name,
			"post_modified_gmt": now,
		},
	})
	if err != nil {
		return nil, errors.Wrapf(err, "rename passkey for user %s", user.ID.Hex())
	}
	if result.ModifiedCount == 0 {
		return nil, errors.New("passkey credential not found")
	}

	for idx := range user.Passkeys {
		if user.Passkeys[idx].ID == credentialID {
			user.Passkeys[idx].Name = name
			break
		}
	}
	user.ModifiedAt = now
	return user, nil
}

// DeletePasskeyCredential removes one passkey from the authenticated user.
// It accepts a context, user, and credential ID, returning the updated user.
func (s *Blog) DeletePasskeyCredential(ctx context.Context,
	user *model.User,
	credentialID string,
) (*model.User, error) {
	if user == nil {
		return nil, errors.New("user is nil")
	}
	credentialID = strings.TrimSpace(credentialID)
	if credentialID == "" {
		return nil, errors.New("passkey credential id is empty")
	}

	now := gutils.Clock.GetUTCNow()
	result, err := s.dao.GetUsersCol().UpdateOne(ctx, bson.M{
		"_id":         user.ID,
		"passkeys.id": credentialID,
	}, bson.M{
		"$pull": bson.M{"passkeys": bson.M{"id": credentialID}},
		"$set":  bson.M{"post_modified_gmt": now},
	})
	if err != nil {
		return nil, errors.Wrapf(err, "delete passkey for user %s", user.ID.Hex())
	}
	if result.ModifiedCount == 0 {
		return nil, errors.New("passkey credential not found")
	}

	nextPasskeys := make([]model.PasskeyCredential, 0, len(user.Passkeys))
	for _, passkey := range user.Passkeys {
		if passkey.ID != credentialID {
			nextPasskeys = append(nextPasskeys, passkey)
		}
	}
	user.Passkeys = nextPasskeys
	user.ModifiedAt = now
	return user, nil
}
