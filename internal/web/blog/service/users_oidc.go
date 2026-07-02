package service

import (
	"context"
	"strings"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

// FindUserByOIDCIdentity loads a user by external identity provider and subject.
// It accepts a context, provider name, and provider subject, returning the matched user.
func (s *Blog) FindUserByOIDCIdentity(ctx context.Context, provider string, subject string) (*model.User, error) {
	provider = strings.TrimSpace(strings.ToLower(provider))
	subject = strings.TrimSpace(subject)
	if provider == "" || subject == "" {
		return nil, errors.New("provider and subject are required")
	}

	user := new(model.User)
	err := s.dao.GetUsersCol().FindOne(ctx, bson.M{
		"oidc_identities": bson.M{
			"$elemMatch": bson.M{
				"provider": provider,
				"subject":  subject,
			},
		},
	}).Decode(user)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, errors.WithStack(mongo.ErrNoDocuments)
		}
		return nil, errors.Wrap(err, "find user by oidc identity")
	}

	return user, nil
}

// FindUserByAccount loads a user by sanitized account value.
// It accepts a context and account, returning the matched user.
func (s *Blog) FindUserByAccount(ctx context.Context, account string) (*model.User, error) {
	account, err := sanitizeUserAccount(account)
	if err != nil {
		return nil, errors.Wrap(err, "sanitize account")
	}

	user := new(model.User)
	if err = s.dao.GetUsersCol().FindOne(ctx, bson.M{"account": account}).Decode(user); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, errors.WithStack(mongo.ErrNoDocuments)
		}
		return nil, errors.Wrapf(err, "find user %q", account)
	}

	return user, nil
}

// GetOrCreateOIDCUser finds, binds, or creates a user for an external OIDC identity.
// It accepts provider identity fields and returns an active local user linked to that identity.
func (s *Blog) GetOrCreateOIDCUser(ctx context.Context,
	provider string,
	subject string,
	email string,
	displayName string,
) (*model.User, error) {
	provider = strings.TrimSpace(strings.ToLower(provider))
	subject = strings.TrimSpace(subject)
	email = strings.TrimSpace(strings.ToLower(email))
	displayName = strings.TrimSpace(displayName)
	if provider == "" || subject == "" {
		return nil, errors.New("provider and subject are required")
	}
	if email == "" {
		return nil, errors.New("verified email is required")
	}
	if displayName == "" {
		displayName = email
	}

	user, err := s.FindUserByOIDCIdentity(ctx, provider, subject)
	if err != nil {
		if !errors.Is(err, mongo.ErrNoDocuments) {
			return nil, errors.Wrap(err, "find existing oidc user")
		}
	} else {
		return user, nil
	}

	user, err = s.FindUserByAccount(ctx, email)
	if err != nil {
		if !errors.Is(err, mongo.ErrNoDocuments) {
			return nil, errors.Wrap(err, "find existing account")
		}
		return s.createOIDCUser(ctx, provider, subject, email, displayName)
	}

	return s.BindOIDCIdentityToUser(ctx, user, provider, subject, email)
}

// createOIDCUser creates a new active user linked to an external OIDC identity.
// It accepts provider identity fields and returns the inserted user.
func (s *Blog) createOIDCUser(ctx context.Context,
	provider string,
	subject string,
	email string,
	displayName string,
) (*model.User, error) {
	now := gutils.Clock.GetUTCNow()
	user := model.NewUser()
	user.Account = email
	user.Username = displayName
	user.Status = model.UserStatusActive
	user.ActiveToken = ""
	user.ModifiedAt = now
	user.OIDCIdentities = []model.OIDCIdentity{{
		Provider: provider,
		Subject:  subject,
		Email:    email,
		BoundAt:  now,
	}}

	if _, err := s.dao.GetUsersCol().InsertOne(ctx, user); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			existing, findErr := s.FindUserByAccount(ctx, email)
			if findErr != nil {
				return nil, errors.Wrap(findErr, "find duplicate oidc account")
			}
			return s.BindOIDCIdentityToUser(ctx, existing, provider, subject, email)
		}
		return nil, errors.Wrapf(err, "insert oidc user %q", email)
	}

	return user, nil
}

// BindOIDCIdentityToUser links an existing local user to an external OIDC identity.
// It accepts the user and identity fields, returning the updated user.
func (s *Blog) BindOIDCIdentityToUser(ctx context.Context,
	user *model.User,
	provider string,
	subject string,
	email string,
) (*model.User, error) {
	if user == nil {
		return nil, errors.New("user is nil")
	}
	provider = strings.TrimSpace(strings.ToLower(provider))
	subject = strings.TrimSpace(subject)
	email = strings.TrimSpace(strings.ToLower(email))
	if provider == "" || subject == "" {
		return nil, errors.New("provider and subject are required")
	}

	existingUser, err := s.FindUserByOIDCIdentity(ctx, provider, subject)
	if err != nil {
		if !errors.Is(err, mongo.ErrNoDocuments) {
			return nil, errors.Wrap(err, "find oidc identity owner")
		}
	} else {
		if existingUser.ID != user.ID {
			return nil, errors.New("oidc identity is already bound to another user")
		}
		return s.EnsureUserUID(ctx, existingUser)
	}

	for _, identity := range user.OIDCIdentities {
		if identity.Provider == provider && identity.Subject == subject {
			return user, nil
		}
	}

	now := gutils.Clock.GetUTCNow()
	identity := model.OIDCIdentity{
		Provider: provider,
		Subject:  subject,
		Email:    email,
		BoundAt:  now,
	}
	result, err := s.dao.GetUsersCol().UpdateOne(ctx, bson.M{
		"_id": user.ID,
		"oidc_identities": bson.M{
			"$not": bson.M{
				"$elemMatch": bson.M{
					"provider": provider,
					"subject":  subject,
				},
			},
		},
	}, bson.M{
		"$push": bson.M{"oidc_identities": identity},
		"$set":  bson.M{"post_modified_gmt": now},
	})
	if err != nil {
		return nil, errors.Wrapf(err, "bind oidc identity for user %s", user.ID.Hex())
	}

	if result.ModifiedCount > 0 {
		user.OIDCIdentities = append(user.OIDCIdentities, identity)
		user.ModifiedAt = now
	}

	return user, nil
}
