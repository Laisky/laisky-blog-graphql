package controller

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

const (
	passkeySessionKindRegister = "passkey_register"
	passkeySessionKindLogin    = "passkey_login"
	passkeySessionTTL          = 10 * time.Minute
)

// passkeySessionEnvelope stores a signed WebAuthn ceremony session.
// It contains the ceremony kind, redirect target, WebAuthn session data, and expiration timestamp.
type passkeySessionEnvelope struct {
	Kind       string               `json:"kind"`
	RedirectTo string               `json:"redirect_to,omitempty"`
	Session    webauthn.SessionData `json:"session"`
	ExpiresAt  int64                `json:"expires_at"`
}

// passkeyUser adapts the blog user model to the WebAuthn user interface.
// It wraps a blog user and exposes stable user handle and credential records.
type passkeyUser struct {
	user *model.User
}

// WebAuthnID returns the stable WebAuthn user handle.
// It accepts no parameters and returns the Mongo ObjectID hex value as bytes.
func (u passkeyUser) WebAuthnID() []byte {
	return []byte(u.user.UID)
}

// WebAuthnName returns the account identifier shown to authenticators.
// It accepts no parameters and returns the user's account string.
func (u passkeyUser) WebAuthnName() string {
	return u.user.Account
}

// WebAuthnDisplayName returns the user-visible display name.
// It accepts no parameters and returns the user's display name.
func (u passkeyUser) WebAuthnDisplayName() string {
	return u.user.Username
}

// WebAuthnCredentials returns the WebAuthn credentials owned by the user.
// It accepts no parameters and returns decoded credential records.
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential {
	credentials := make([]webauthn.Credential, 0, len(u.user.Passkeys))
	for _, passkey := range u.user.Passkeys {
		credential, err := decodeStoredPasskeyCredential(passkey)
		if err != nil {
			continue
		}
		credentials = append(credentials, credential)
	}

	return credentials
}

// UserStartPasskeyRegistration starts a passkey registration ceremony for the authenticated user.
// It accepts a label for the new credential and returns browser options plus a signed session.
func (r *MutationResolver) UserStartPasskeyRegistration(ctx context.Context, label string) (*models.PasskeyStartResponse, error) {
	user, err := r.svc.ValidateAndGetUser(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "validate user")
	}
	if err = validateInputLength(100, label); err != nil {
		return nil, errors.Wrap(err, "validate passkey label")
	}

	wa, err := newWebAuthnForRequest(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "create webauthn")
	}

	creation, session, err := wa.BeginRegistration(passkeyUser{user: user},
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			UserVerification: protocol.VerificationPreferred,
		}),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithExclusions(webauthn.Credentials(passkeyUser{user: user}.WebAuthnCredentials()).CredentialDescriptors()),
	)
	if err != nil {
		return nil, errors.Wrap(err, "begin passkey registration")
	}

	optionsJSON, err := json.Marshal(creation)
	if err != nil {
		return nil, errors.Wrap(err, "marshal passkey registration options")
	}
	signedSession, err := signPasskeySession(passkeySessionEnvelope{
		Kind:      passkeySessionKindRegister,
		Session:   *session,
		ExpiresAt: gutils.Clock.GetUTCNow().Add(passkeySessionTTL).Unix(),
	})
	if err != nil {
		return nil, errors.Wrap(err, "sign passkey registration session")
	}

	return &models.PasskeyStartResponse{
		OptionsJSON: string(optionsJSON),
		Session:     signedSession,
	}, nil
}

// UserFinishPasskeyRegistration finishes a passkey registration ceremony for the authenticated user.
// It accepts the label, signed session, and browser credential JSON, returning the updated profile.
func (r *MutationResolver) UserFinishPasskeyRegistration(ctx context.Context,
	label string,
	session string,
	credentialJSON string,
) (*models.SsoProfile, error) {
	user, err := r.svc.ValidateAndGetUser(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "validate user")
	}
	if err = validateInputLength(100, label); err != nil {
		return nil, errors.Wrap(err, "validate passkey label")
	}
	if err = validateInputLength(64_000, session, credentialJSON); err != nil {
		return nil, errors.Wrap(err, "validate passkey registration input")
	}

	envelope, err := verifyPasskeySession(session, passkeySessionKindRegister)
	if err != nil {
		return nil, errors.Wrap(err, "verify passkey registration session")
	}

	wa, err := newWebAuthnForRequest(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "create webauthn")
	}
	request, err := newWebAuthnCredentialRequest(ctx, credentialJSON)
	if err != nil {
		return nil, errors.Wrap(err, "create passkey registration request")
	}
	credential, err := wa.FinishRegistration(passkeyUser{user: user}, envelope.Session, request)
	if err != nil {
		return nil, errors.Wrap(err, "finish passkey registration")
	}

	updatedUser, err := r.svc.AddPasskeyCredential(ctx, user, label, credential)
	if err != nil {
		return nil, errors.Wrap(err, "store passkey credential")
	}

	return newSSOProfile(updatedUser), nil
}

// UserRenamePasskey updates the label for one authenticated user's passkey.
// It accepts the passkey credential ID and new name, returning the updated SSO profile.
func (r *MutationResolver) UserRenamePasskey(ctx context.Context, passkeyID string, name string) (*models.SsoProfile, error) {
	user, err := r.svc.ValidateAndGetUser(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "validate user")
	}
	if err = validateInputLength(1024, passkeyID); err != nil {
		return nil, errors.Wrap(err, "validate passkey id")
	}
	if err = validateInputLength(100, name); err != nil {
		return nil, errors.Wrap(err, "validate passkey name")
	}

	updatedUser, err := r.svc.RenamePasskeyCredential(ctx, user, passkeyID, name)
	if err != nil {
		return nil, errors.Wrap(err, "rename passkey")
	}

	return newSSOProfile(updatedUser), nil
}

// UserDeletePasskey removes one authenticated user's passkey.
// It accepts the passkey credential ID and returns the updated SSO profile.
func (r *MutationResolver) UserDeletePasskey(ctx context.Context, passkeyID string) (*models.SsoProfile, error) {
	user, err := r.svc.ValidateAndGetUser(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "validate user")
	}
	if err = validateInputLength(1024, passkeyID); err != nil {
		return nil, errors.Wrap(err, "validate passkey id")
	}

	updatedUser, err := r.svc.DeletePasskeyCredential(ctx, user, passkeyID)
	if err != nil {
		return nil, errors.Wrap(err, "delete passkey")
	}

	return newSSOProfile(updatedUser), nil
}

// UserStartPasskeyLogin starts a discoverable passkey login ceremony.
// It accepts an optional redirect target and Turnstile token, returning browser options plus a signed session.
func (r *MutationResolver) UserStartPasskeyLogin(ctx context.Context,
	redirectTo *string,
	turnstileToken *string,
) (*models.PasskeyStartResponse, error) {
	if err := validateTurnstileTokenForLogin(ctx, turnstileToken); err != nil {
		if errors.Is(err, model.ErrTurnstileRequired) {
			return nil, errors.WithStack(model.ErrTurnstileRequired)
		}
		return nil, maskLoginError(model.ErrInvalidCredentials)
	}
	validatedRedirect, err := resolveGitHubOAuthRedirectTarget(ctx, redirectTo)
	if err != nil {
		return nil, errors.Wrap(err, "validate redirect target")
	}

	wa, err := newWebAuthnForRequest(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "create webauthn")
	}
	assertion, session, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationPreferred))
	if err != nil {
		return nil, errors.Wrap(err, "begin passkey login")
	}

	optionsJSON, err := json.Marshal(assertion)
	if err != nil {
		return nil, errors.Wrap(err, "marshal passkey login options")
	}
	signedSession, err := signPasskeySession(passkeySessionEnvelope{
		Kind:       passkeySessionKindLogin,
		RedirectTo: validatedRedirect,
		Session:    *session,
		ExpiresAt:  gutils.Clock.GetUTCNow().Add(passkeySessionTTL).Unix(),
	})
	if err != nil {
		return nil, errors.Wrap(err, "sign passkey login session")
	}

	return &models.PasskeyStartResponse{
		OptionsJSON: string(optionsJSON),
		Session:     signedSession,
	}, nil
}

// UserFinishPasskeyLogin finishes a discoverable passkey login ceremony.
// It accepts the signed session and browser credential JSON, returning an SSO token and redirect target.
func (r *MutationResolver) UserFinishPasskeyLogin(ctx context.Context,
	session string,
	credentialJSON string,
) (*models.PasskeyLoginResponse, error) {
	if err := validateInputLength(64_000, session, credentialJSON); err != nil {
		return nil, errors.Wrap(err, "validate passkey login input")
	}

	envelope, err := verifyPasskeySession(session, passkeySessionKindLogin)
	if err != nil {
		return nil, maskLoginError(err)
	}

	wa, err := newWebAuthnForRequest(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "create webauthn")
	}
	request, err := newWebAuthnCredentialRequest(ctx, credentialJSON)
	if err != nil {
		return nil, maskLoginError(err)
	}

	var authenticatedUser *model.User
	handler := func(rawID []byte, userHandle []byte) (webauthn.User, error) {
		user, loadErr := r.loadPasskeyUser(ctx, rawID, userHandle)
		if loadErr != nil {
			return nil, loadErr
		}
		authenticatedUser = user
		return passkeyUser{user: user}, nil
	}
	_, credential, err := wa.FinishPasskeyLogin(handler, envelope.Session, request)
	if err != nil {
		return nil, maskLoginError(err)
	}
	if authenticatedUser == nil {
		return nil, maskLoginError(model.ErrInvalidCredentials)
	}

	updatedUser, err := r.svc.UpdatePasskeyCredential(ctx, authenticatedUser, credential)
	if err != nil {
		return nil, errors.Wrap(err, "update passkey credential")
	}
	loginResp, err := r.newLoginResponse(ctx, updatedUser)
	if err != nil {
		return nil, errors.Wrap(err, "create passkey login response")
	}

	return &models.PasskeyLoginResponse{
		User:       loginResp.User,
		Token:      loginResp.Token,
		RedirectTo: envelope.RedirectTo,
	}, nil
}

// loadPasskeyUser loads the user for a WebAuthn discoverable credential callback.
// It accepts raw credential ID and user handle, returning the matching blog user.
func (r *MutationResolver) loadPasskeyUser(ctx context.Context, rawID []byte, userHandle []byte) (*model.User, error) {
	if len(rawID) > 0 {
		user, err := r.svc.FindUserByPasskeyID(ctx, rawID)
		if err != nil {
			return nil, errors.Wrap(err, "find user by passkey id")
		}
		return user, nil
	}
	if len(userHandle) == 0 {
		return nil, errors.New("passkey user handle is empty")
	}

	handle := strings.TrimSpace(string(userHandle))
	id, err := primitive.ObjectIDFromHex(handle)
	if err != nil {
		user, loadErr := r.svc.LoadUserByUID(ctx, handle)
		if loadErr != nil {
			return nil, errors.Wrap(loadErr, "load passkey user by uid handle")
		}
		return user, nil
	}
	user, err := r.svc.LoadUserByID(ctx, id)
	if err != nil {
		return nil, errors.Wrap(err, "load passkey user by handle")
	}
	return user, nil
}

// newWebAuthnForRequest creates a WebAuthn handler for the current request origin.
// It accepts request context and returns a configured WebAuthn handler.
func newWebAuthnForRequest(ctx context.Context) (*webauthn.WebAuthn, error) {
	origin := resolveRequestBaseURL(ctx)
	if configured := strings.TrimSpace(gconfig.Shared.GetString("settings.web.webauthn.origin")); configured != "" {
		origin = configured
	}
	if origin == "" {
		return nil, errors.New("webauthn origin is unavailable")
	}

	rpID := strings.TrimSpace(gconfig.Shared.GetString("settings.web.webauthn.rp_id"))
	if rpID == "" {
		parsed, err := url.Parse(origin)
		if err != nil {
			return nil, errors.Wrap(err, "parse webauthn origin")
		}
		host := parsed.Hostname()
		if net.ParseIP(host) != nil {
			rpID = host
		} else {
			rpID = strings.TrimSuffix(host, ".")
		}
	}

	displayName := strings.TrimSpace(gconfig.Shared.GetString("settings.web.webauthn.rp_display_name"))
	if displayName == "" {
		displayName = "Laisky SSO"
	}

	wa, err := webauthn.New(&webauthn.Config{
		RPDisplayName: displayName,
		RPID:          rpID,
		RPOrigins:     []string{origin},
	})
	if err != nil {
		return nil, errors.Wrap(err, "initialize webauthn")
	}

	return wa, nil
}

// newWebAuthnCredentialRequest builds an HTTP request for the WebAuthn parser.
// It accepts context and credential JSON, returning a POST request with the JSON body.
func newWebAuthnCredentialRequest(ctx context.Context, credentialJSON string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/webauthn/finish", bytes.NewBufferString(credentialJSON))
	if err != nil {
		return nil, errors.Wrap(err, "create webauthn credential request")
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// decodeStoredPasskeyCredential decodes a persisted passkey credential.
// It accepts a stored passkey model and returns the WebAuthn credential record.
func decodeStoredPasskeyCredential(passkey model.PasskeyCredential) (webauthn.Credential, error) {
	if strings.TrimSpace(passkey.CredentialJSON) != "" {
		var credential webauthn.Credential
		if err := json.Unmarshal([]byte(passkey.CredentialJSON), &credential); err != nil {
			return credential, errors.Wrap(err, "unmarshal passkey credential")
		}
		return credential, nil
	}

	id, err := base64.RawURLEncoding.DecodeString(passkey.ID)
	if err != nil {
		return webauthn.Credential{}, errors.Wrap(err, "decode passkey id")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(passkey.PublicKey)
	if err != nil {
		return webauthn.Credential{}, errors.Wrap(err, "decode passkey public key")
	}
	var aaguid []byte
	if strings.TrimSpace(passkey.AAGUID) != "" {
		aaguid, err = base64.RawURLEncoding.DecodeString(passkey.AAGUID)
		if err != nil {
			return webauthn.Credential{}, errors.Wrap(err, "decode passkey aaguid")
		}
	}
	transports := make([]protocol.AuthenticatorTransport, 0)
	for _, rawTransport := range strings.Split(passkey.Transport, ",") {
		rawTransport = strings.TrimSpace(rawTransport)
		if rawTransport != "" {
			transports = append(transports, protocol.AuthenticatorTransport(rawTransport))
		}
	}

	return webauthn.Credential{
		ID:              id,
		PublicKey:       publicKey,
		AttestationType: passkey.AttestationType,
		Transport:       transports,
		Flags: webauthn.CredentialFlags{
			BackupEligible: passkey.BackupEligible,
			BackupState:    passkey.BackupState,
		},
		Authenticator: webauthn.Authenticator{
			AAGUID:    aaguid,
			SignCount: passkey.SignCount,
		},
	}, nil
}

// signPasskeySession signs a WebAuthn session envelope for client transport.
// It accepts a passkey session envelope and returns an encoded signed session string.
func signPasskeySession(payload passkeySessionEnvelope) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", errors.Wrap(err, "marshal passkey session")
	}
	signature, err := signPasskeyBytes(raw)
	if err != nil {
		return "", errors.Wrap(err, "sign passkey session")
	}
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// verifyPasskeySession verifies a signed WebAuthn session envelope.
// It accepts the encoded session and expected kind, returning the decoded envelope when valid.
func verifyPasskeySession(encoded string, expectedKind string) (*passkeySessionEnvelope, error) {
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 {
		return nil, errors.New("invalid passkey session format")
	}

	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.Wrap(err, "decode passkey session")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.Wrap(err, "decode passkey session signature")
	}
	expectedSignature, err := signPasskeyBytes(raw)
	if err != nil {
		return nil, errors.Wrap(err, "sign passkey session for comparison")
	}
	if subtle.ConstantTimeCompare(signature, expectedSignature) != 1 {
		return nil, errors.New("invalid passkey session signature")
	}

	var payload passkeySessionEnvelope
	if err = json.Unmarshal(raw, &payload); err != nil {
		return nil, errors.Wrap(err, "unmarshal passkey session")
	}
	if payload.Kind != expectedKind {
		return nil, errors.New("invalid passkey session kind")
	}
	if payload.ExpiresAt < gutils.Clock.GetUTCNow().Unix() {
		return nil, errors.New("passkey session expired")
	}

	return &payload, nil
}

// signPasskeyBytes signs raw passkey session bytes with the application secret.
// It accepts raw payload bytes and returns the HMAC-SHA256 signature.
func signPasskeyBytes(raw []byte) ([]byte, error) {
	secret := strings.TrimSpace(gconfig.Shared.GetString("settings.secret"))
	if secret == "" {
		return nil, errors.New("settings.secret is required for passkey session")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write(raw); err != nil {
		return nil, errors.Wrap(err, "write passkey hmac payload")
	}
	return mac.Sum(nil), nil
}
