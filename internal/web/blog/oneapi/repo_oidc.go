package oneapi

import (
	"context"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/Laisky/zap"
	"gorm.io/gorm"

	blogmodel "github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

// FindByOIDCIdentity resolves an SSO provider subject to its OneAPI user.
func (r *Repo) FindByOIDCIdentity(ctx context.Context, provider string, subject string) (*blogmodel.User, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	subject = strings.TrimSpace(subject)
	if provider == "" || len(provider) > 32 || subject == "" || len(subject) > 255 {
		return nil, errors.New("oidc provider and subject are required")
	}
	var binding SSOOIDCIdentity
	err := r.db.WithContext(ctx).Where("provider = ? AND subject = ?", provider, subject).First(&binding).Error
	if err == nil {
		return r.GetByID(ctx, binding.UserID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.Wrap(err, "find sso oidc identity")
	}
	if provider != oidcProviderGitHub {
		return nil, errors.WithStack(ErrNotFound)
	}

	var users []User
	if err = r.db.WithContext(ctx).Where("github_id = ?", subject).Limit(2).Find(&users).Error; err != nil {
		return nil, errors.Wrap(err, "find oneapi github identity")
	}
	if len(users) == 0 {
		return nil, errors.WithStack(ErrNotFound)
	}
	if len(users) > 1 {
		return nil, errors.New("github identity is assigned to multiple oneapi users")
	}
	if err = r.bindOIDCIdentity(ctx, users[0].ID, provider, subject, users[0].Email); err != nil {
		return nil, errors.Wrap(err, "adopt oneapi github identity")
	}
	return r.toBlogUser(ctx, &users[0])
}

// BindOIDCIdentity assigns a provider subject to an existing OneAPI user.
func (r *Repo) BindOIDCIdentity(ctx context.Context, userID int, provider string, subject string, email string) (*blogmodel.User, error) {
	if err := r.bindOIDCIdentity(ctx, userID, provider, subject, email); err != nil {
		return nil, err
	}
	return r.GetByID(ctx, userID)
}

func (r *Repo) bindOIDCIdentity(ctx context.Context, userID int, provider string, subject string, email string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	subject = strings.TrimSpace(subject)
	if userID <= 0 || provider == "" || len(provider) > 32 || subject == "" || len(subject) > 255 {
		return errors.New("oidc user, provider, and subject are required")
	}
	if provider != oidcProviderGitHub {
		return errors.Errorf("unsupported oidc provider %q", provider)
	}

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing SSOOIDCIdentity
		queryErr := tx.Where("provider = ? AND subject = ?", provider, subject).First(&existing).Error
		if queryErr == nil {
			if existing.UserID != userID {
				return errors.New("oidc identity is already bound to another user")
			}
			return nil
		}
		if !errors.Is(queryErr, gorm.ErrRecordNotFound) {
			return errors.Wrap(queryErr, "check oidc identity owner")
		}
		var count int64
		if queryErr = tx.Model(&SSOOIDCIdentity{}).Where("provider = ? AND user_id = ?", provider, userID).Count(&count).Error; queryErr != nil {
			return errors.Wrap(queryErr, "check user's oidc identity")
		}
		if count > 0 {
			return errors.New("user already has an identity for this provider")
		}

		now := time.Now().UTC()
		binding := SSOOIDCIdentity{
			Provider:  provider,
			Subject:   subject,
			UserID:    userID,
			Email:     strings.TrimSpace(email),
			BoundAt:   now,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if createErr := tx.Create(&binding).Error; createErr != nil {
			if errors.Is(createErr, gorm.ErrDuplicatedKey) {
				return errors.New("oidc identity binding conflict")
			}
			return errors.Wrap(createErr, "create oidc identity binding")
		}
		update := tx.Model(&User{}).
			Where("id = ? AND (github_id IS NULL OR github_id = '' OR github_id = ?)", userID, subject).
			Update("github_id", subject)
		if update.Error != nil {
			return errors.Wrap(update.Error, "mirror github identity to oneapi user")
		}
		if update.RowsAffected == 0 {
			return errors.New("oneapi user already has a different github identity")
		}
		return nil
	})
	if err != nil {
		return errors.WithStack(err)
	}
	return nil
}

// CreateOIDCUser creates an enabled passwordless OneAPI user and binds its
// external identity atomically.
func (r *Repo) CreateOIDCUser(ctx context.Context, provider string, subject string, email string, displayName string) (*blogmodel.User, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	subject = strings.TrimSpace(subject)
	if provider != oidcProviderGitHub || subject == "" || len(subject) > 255 {
		return nil, errors.New("a github subject is required")
	}
	quota, err := r.newUserQuota(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "load oneapi new-user quota")
	}
	affCode, err := r.generateUniqueAffCode(ctx)
	if err != nil {
		return nil, err
	}
	user := &User{
		UUID:        gutils.UUID7(),
		Username:    usernameForEmail(email),
		DisplayName: displayName,
		Role:        RoleCommonUser,
		Status:      StatusEnabled,
		Email:       email,
		GitHubID:    subject,
		AccessToken: strings.ReplaceAll(gutils.UUID7(), "-", ""),
		AffCode:     affCode,
		Quota:       quota,
	}
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if queryErr := tx.Model(&User{}).Where("email = ? OR username = ?", email, user.Username).Count(&count).Error; queryErr != nil {
			return errors.Wrap(queryErr, "check oidc account")
		}
		if count > 0 {
			return errors.WithStack(blogmodel.ErrAccountExists)
		}
		if createErr := tx.Create(user).Error; createErr != nil {
			if errors.Is(createErr, gorm.ErrDuplicatedKey) {
				return errors.WithStack(blogmodel.ErrAccountExists)
			}
			return errors.Wrap(createErr, "create oneapi oidc user")
		}
		now := time.Now().UTC()
		link := SSOUserLink{OneAPIUserID: user.ID, OneAPIUserUUID: user.UUID, SSOUID: user.UUID,
			BlogObjectID: blogmodel.SyntheticObjectID(user.ID).Hex(), CreatedAt: now, UpdatedAt: now}
		if createErr := tx.Create(&link).Error; createErr != nil {
			return errors.Wrap(createErr, "create oidc sso user link")
		}
		binding := SSOOIDCIdentity{Provider: provider, Subject: subject, UserID: user.ID,
			Email: email, BoundAt: now, CreatedAt: now, UpdatedAt: now}
		if createErr := tx.Create(&binding).Error; createErr != nil {
			return errors.Wrap(createErr, "create oidc binding")
		}
		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if err = r.createDefaultToken(ctx, user); err != nil {
		r.logger.Warn("create default token for oidc sso user", zap.Int("user_id", user.ID), zap.Error(err))
	}
	return r.toBlogUser(ctx, user)
}
