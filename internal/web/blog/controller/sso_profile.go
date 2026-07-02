package controller

import (
	"context"

	"github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	"github.com/Laisky/laisky-blog-graphql/library"
)

// UserProfile returns the authenticated user's SSO profile state.
// It accepts a request context and returns the profile visible to the current user.
func (r *QueryResolver) UserProfile(ctx context.Context) (*models.SsoProfile, error) {
	user, err := r.svc.ValidateAndGetUser(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "validate user")
	}

	return newSSOProfile(user), nil
}

// UserChangePassword changes the authenticated user's password.
// It accepts the current password and desired new password, returning the updated profile.
func (r *MutationResolver) UserChangePassword(ctx context.Context, currentPassword string, newPassword string) (*models.SsoProfile, error) {
	user, err := r.svc.ValidateAndGetUser(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "validate user")
	}

	user, err = r.svc.ChangePassword(ctx, user, currentPassword, newPassword)
	if err != nil {
		return nil, errors.Wrap(err, "change password")
	}

	return newSSOProfile(user), nil
}

// UserStartTOTPSetup creates a TOTP enrollment secret for the authenticated user.
// It accepts a request context and returns the secret plus provisioning URI.
func (r *MutationResolver) UserStartTOTPSetup(ctx context.Context) (*models.TotpSetupResponse, error) {
	user, err := r.svc.ValidateAndGetUser(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "validate user")
	}

	secret, provisioningURI, err := r.svc.StartTOTPSetup(ctx, user)
	if err != nil {
		return nil, errors.Wrap(err, "start totp setup")
	}

	return &models.TotpSetupResponse{
		Secret:          secret,
		ProvisioningURI: provisioningURI,
	}, nil
}

// UserConfirmTOTPSetup enables TOTP for the authenticated user after code verification.
// It accepts the current TOTP code and returns the updated profile.
func (r *MutationResolver) UserConfirmTOTPSetup(ctx context.Context, code string) (*models.SsoProfile, error) {
	user, err := r.svc.ValidateAndGetUser(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "validate user")
	}

	user, err = r.svc.ConfirmTOTPSetup(ctx, user, code)
	if err != nil {
		return nil, errors.Wrap(err, "confirm totp setup")
	}

	return newSSOProfile(user), nil
}

// UserDisableTotp disables TOTP for the authenticated user after password verification.
// It accepts the current password and returns the updated profile.
func (r *MutationResolver) UserDisableTotp(ctx context.Context, currentPassword string) (*models.SsoProfile, error) {
	user, err := r.svc.ValidateAndGetUser(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "validate user")
	}

	user, err = r.svc.DisableTOTP(ctx, user, currentPassword)
	if err != nil {
		return nil, errors.Wrap(err, "disable totp")
	}

	return newSSOProfile(user), nil
}

// newSSOProfile builds a public SSO profile from a user record.
// It accepts a user model and returns the GraphQL profile object.
func newSSOProfile(user *model.User) *models.SsoProfile {
	authMethods := []string{"password"}
	if user.TOTPEnabled {
		authMethods = append(authMethods, "totp")
	}
	if len(user.Passkeys) > 0 {
		authMethods = append(authMethods, "passkey")
	}
	githubBound := false
	for _, identity := range user.OIDCIdentities {
		if identity.Provider == "github" {
			githubBound = true
			break
		}
	}
	passkeys := make([]*models.PasskeyInfo, 0, len(user.Passkeys))
	for _, passkey := range user.Passkeys {
		createdAt := library.NewDatetimeFromTime(passkey.CreatedAt)
		passkeys = append(passkeys, &models.PasskeyInfo{
			ID:        passkey.ID,
			Name:      passkey.Name,
			CreatedAt: *createdAt,
		})
	}

	return &models.SsoProfile{
		User:            user,
		UID:             user.UID,
		Account:         user.Account,
		AuthMethods:     authMethods,
		PasswordEnabled: user.Password != "",
		TotpEnabled:     user.TOTPEnabled,
		PasskeyCount:    len(user.Passkeys),
		Passkeys:        passkeys,
		GithubBound:     githubBound,
	}
}
