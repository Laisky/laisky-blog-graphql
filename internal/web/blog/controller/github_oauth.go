package controller

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	gconfig "github.com/Laisky/go-config/v2"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/Laisky/zap"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"github.com/Laisky/laisky-blog-graphql/internal/library/ssoredirect"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	"github.com/Laisky/laisky-blog-graphql/library/clientip"
)

const (
	githubOAuthAuthorizeEndpoint = "https://github.com/login/oauth/authorize"
	githubOAuthProvider          = "github"
	githubOAuthStateKindLogin    = "login"
	githubOAuthStateKindBind     = "bind"
	githubOAuthStateTTL          = 10 * time.Minute
	githubOAuthHTTPTimeout       = 8 * time.Second

	githubOAuthClientIDConfigKey     = "settings.web.github_oauth.client_id"
	githubOAuthClientSecretConfigKey = "settings.web.github_oauth.client_secret"
)

var githubOAuthHTTPClient = &http.Client{
	Timeout: githubOAuthHTTPTimeout,
}

// githubOAuthState stores the signed state passed through GitHub OAuth.
// It contains the validated redirect target, callback URL, nonce, and expiration timestamp.
type githubOAuthState struct {
	RedirectTo  string `json:"redirect_to"`
	CallbackURL string `json:"callback_url"`
	Kind        string `json:"kind,omitempty"`
	UserUID     string `json:"user_uid,omitempty"`
	Nonce       string `json:"nonce"`
	ExpiresAt   int64  `json:"expires_at"`
}

// githubOAuthUser describes the public GitHub user response.
// It contains the stable GitHub ID, login name, display name, and public email.
type githubOAuthUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// githubOAuthEmail describes a GitHub email address record.
// It contains the email address plus primary and verification flags.
type githubOAuthEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

// UserGithubOAuthStart starts the GitHub OAuth login or registration flow.
// It accepts an optional redirect target and Turnstile token, returning the GitHub authorization URL.
func (r *MutationResolver) UserGithubOAuthStart(ctx context.Context,
	redirectTo *string,
	turnstileToken *string,
) (*models.GithubOAuthStartResponse, error) {
	attempt, err := admitAuthRequest(ctx, turnstileToken, loginFlow(""))
	if err != nil {
		return nil, turnstileGateError(err)
	}
	defer attempt.finish()

	settings, err := loadGitHubOAuthSettings(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "load github oauth settings")
	}

	// GitHub returns to the configured callback, which may be the other issuer origin
	// (sso vs mcp). Judge the target against that completing origin now, before the user
	// leaves for GitHub, exactly as the completion re-check will.
	validatedRedirect, err := resolveSSORedirectTargetFor(ctx, redirectTo, completionOrigin(ctx, settings.RedirectURL))
	if err != nil {
		return nil, errors.Wrap(err, "validate redirect target")
	}

	state, err := signGitHubOAuthState(githubOAuthState{
		RedirectTo:  validatedRedirect,
		CallbackURL: settings.RedirectURL,
		Kind:        githubOAuthStateKindLogin,
		Nonce:       gutils.UUID7(),
		ExpiresAt:   gutils.Clock.GetUTCNow().Add(githubOAuthStateTTL).Unix(),
	})
	if err != nil {
		return nil, errors.Wrap(err, "sign github oauth state")
	}

	return &models.GithubOAuthStartResponse{
		AuthorizeURL: settings.Config.AuthCodeURL(state, oauth2.AccessTypeOnline),
	}, nil
}

// UserGithubOAuthBindStart starts GitHub OAuth binding for the authenticated user.
// It accepts a request context and returns the GitHub authorization URL.
func (r *MutationResolver) UserGithubOAuthBindStart(ctx context.Context) (*models.GithubOAuthStartResponse, error) {
	user, err := r.svc.ValidateAndGetUser(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "validate user")
	}
	user, err = r.svc.EnsureUserUID(ctx, user)
	if err != nil {
		return nil, errors.Wrap(err, "ensure user uid")
	}

	settings, err := loadGitHubOAuthSettings(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "load github oauth settings")
	}
	validatedRedirect, err := resolveGitHubOAuthRedirectTarget(ctx, nil)
	if err != nil {
		return nil, errors.Wrap(err, "validate redirect target")
	}

	state, err := signGitHubOAuthState(githubOAuthState{
		RedirectTo:  validatedRedirect,
		CallbackURL: settings.RedirectURL,
		Kind:        githubOAuthStateKindBind,
		UserUID:     user.UID,
		Nonce:       gutils.UUID7(),
		ExpiresAt:   gutils.Clock.GetUTCNow().Add(githubOAuthStateTTL).Unix(),
	})
	if err != nil {
		return nil, errors.Wrap(err, "sign github oauth bind state")
	}

	return &models.GithubOAuthStartResponse{
		AuthorizeURL: settings.Config.AuthCodeURL(state, oauth2.AccessTypeOnline),
	}, nil
}

// UserGithubOAuthLogin completes the GitHub OAuth callback.
// It accepts the authorization code and signed state, returning an SSO token and validated redirect target.
func (r *MutationResolver) UserGithubOAuthLogin(ctx context.Context,
	code string,
	state string,
) (*models.GithubOAuthLoginResponse, error) {
	if err := validateInputLength(4096, code, state); err != nil {
		return nil, errors.Wrap(err, "validate github oauth callback input")
	}

	payload, err := verifyGitHubOAuthState(state)
	if err != nil {
		return nil, maskLoginError(err)
	}
	if err = ensureSSORedirectStillAllowed(ctx, payload.RedirectTo); err != nil {
		return nil, errors.Wrap(err, "complete github oauth login")
	}

	settings, err := loadGitHubOAuthSettingsWithRedirect(ctx, payload.CallbackURL)
	if err != nil {
		return nil, errors.Wrap(err, "load github oauth settings")
	}

	githubUser, email, err := fetchGitHubOAuthUser(ctx, settings.Config, code)
	if err != nil {
		return nil, maskLoginError(err)
	}

	subject := strconv.FormatInt(githubUser.ID, 10)
	displayName := strings.TrimSpace(githubUser.Name)
	if displayName == "" {
		displayName = strings.TrimSpace(githubUser.Login)
	}
	if displayName == "" {
		displayName = email
	}

	stateKind := strings.TrimSpace(payload.Kind)
	if stateKind == "" {
		stateKind = githubOAuthStateKindLogin
	}

	var user *model.User
	switch stateKind {
	case githubOAuthStateKindLogin:
		user, err = r.svc.GetOrCreateOIDCUser(ctx, githubOAuthProvider, subject, email, displayName)
		if err != nil {
			return nil, errors.Wrap(err, "get or create github user")
		}
	case githubOAuthStateKindBind:
		user, err = r.svc.LoadUserByUID(ctx, payload.UserUID)
		if err != nil {
			return nil, errors.Wrap(err, "load github bind user")
		}
		user, err = r.svc.BindOIDCIdentityToUser(ctx, user, githubOAuthProvider, subject, email)
		if err != nil {
			return nil, errors.Wrap(err, "bind github identity")
		}
	default:
		return nil, errors.New("invalid github oauth state kind")
	}

	loginResp, err := r.newLoginResponse(ctx, user)
	if err != nil {
		return nil, errors.Wrap(err, "create github login response")
	}

	return &models.GithubOAuthLoginResponse{
		User:       loginResp.User,
		Token:      loginResp.Token,
		RedirectTo: payload.RedirectTo,
	}, nil
}

// githubOAuthSettings stores the configured OAuth client and redirect URL.
// It contains the oauth2 config used to build URLs and exchange authorization codes.
type githubOAuthSettings struct {
	Config      *oauth2.Config
	RedirectURL string
}

// loadGitHubOAuthSettings loads OAuth settings for the current request.
// It accepts a context and returns a configured GitHub OAuth client.
func loadGitHubOAuthSettings(ctx context.Context) (*githubOAuthSettings, error) {
	redirectURL := resolveGitHubOAuthCallbackURL(ctx)
	return loadGitHubOAuthSettingsWithRedirect(ctx, redirectURL)
}

// IsGithubOAuthConfigured reports whether GitHub OAuth sign-in is available.
// It returns true only when both the client ID and client secret are configured,
// letting the frontend hide the GitHub option when the administrator has not set it up.
func IsGithubOAuthConfigured() bool {
	clientID := strings.TrimSpace(gconfig.Shared.GetString(githubOAuthClientIDConfigKey))
	clientSecret := strings.TrimSpace(gconfig.Shared.GetString(githubOAuthClientSecretConfigKey))
	return clientID != "" && clientSecret != ""
}

// loadGitHubOAuthSettingsWithRedirect loads OAuth settings with a specific callback URL.
// It accepts a context and callback URL, returning a configured GitHub OAuth client.
func loadGitHubOAuthSettingsWithRedirect(_ context.Context, redirectURL string) (*githubOAuthSettings, error) {
	clientID := strings.TrimSpace(gconfig.Shared.GetString(githubOAuthClientIDConfigKey))
	clientSecret := strings.TrimSpace(gconfig.Shared.GetString(githubOAuthClientSecretConfigKey))
	if clientID == "" || clientSecret == "" {
		return nil, errors.New("github oauth client is not configured")
	}
	if strings.TrimSpace(redirectURL) == "" {
		return nil, errors.New("github oauth redirect url is not configured")
	}

	return &githubOAuthSettings{
		Config: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     github.Endpoint,
			RedirectURL:  redirectURL,
			Scopes:       []string{"read:user", "user:email"},
		},
		RedirectURL: redirectURL,
	}, nil
}

// resolveGitHubOAuthCallbackURL resolves the callback URL for the active request.
// It accepts request context and returns the configured or derived callback URL.
func resolveGitHubOAuthCallbackURL(ctx context.Context) string {
	configured := strings.TrimSpace(gconfig.Shared.GetString("settings.web.github_oauth.redirect_url"))
	if configured != "" {
		return configured
	}

	base := resolveRequestBaseURL(ctx)
	if base == "" {
		return ""
	}

	return strings.TrimRight(base, "/") + "/github/callback"
}

// defaultSSORedirectTarget is the issuer profile page. It stays relative so the page that
// completes the ceremony (for example the configured GitHub callback origin) opens its own
// profile with the session it just stored, without handing a bearer to another origin.
const defaultSSORedirectTarget = "/profile"

// resolveGitHubOAuthRedirectTarget validates and normalizes an optional redirect target for a
// ceremony that completes on the requesting origin, such as passkey login.
// An absent target selects the issuer profile page.
func resolveGitHubOAuthRedirectTarget(ctx context.Context, raw *string) (string, error) {
	return resolveSSORedirectTargetFor(ctx, raw, resolveRequestBaseURL(ctx))
}

// resolveSSORedirectTargetFor validates an optional redirect target for a ceremony that completes
// on completingOrigin. Issuer-relative targets resolve against the requesting origin, the page
// the user is on, and are then judged against the origin that will hand over the session.
func resolveSSORedirectTargetFor(ctx context.Context, raw *string, completingOrigin string) (string, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return defaultSSORedirectTarget, nil
	}

	redirectTo := strings.TrimSpace(*raw)
	if err := validateInputLength(ssoredirect.MaxTargetLength, redirectTo); err != nil {
		return "", errors.Wrap(err, "validate redirect target length")
	}
	if base := resolveRequestBaseURL(ctx); base != "" && strings.HasPrefix(redirectTo, "/") && !strings.HasPrefix(redirectTo, "//") {
		redirectTo = strings.TrimRight(base, "/") + redirectTo
	}
	decision, err := classifySSORedirectTargetFor(ctx, redirectTo, completingOrigin)
	if err != nil {
		return "", errors.Wrap(err, "validate redirect target")
	}
	return decision.Target, nil
}

// completionOrigin returns the origin of the GitHub callback page, which completes the sign-in
// and hands over the session; it falls back to the requesting origin.
func completionOrigin(ctx context.Context, callbackURL string) string {
	if parsed, err := url.Parse(strings.TrimSpace(callbackURL)); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return strings.ToLower(parsed.Scheme) + "://" + parsed.Host
	}
	return resolveRequestBaseURL(ctx)
}

// classifySSORedirectTarget applies the shared SSO redirect policy to raw for a ceremony that
// completes on the requesting origin.
func classifySSORedirectTarget(ctx context.Context, raw string) (ssoredirect.Decision, error) {
	return classifySSORedirectTargetFor(ctx, raw, resolveRequestBaseURL(ctx))
}

// classifySSORedirectTargetFor applies the shared SSO redirect policy (internal/library/ssoredirect)
// to raw as seen from issuerOrigin, the issuer page that hands over the session. Blog targets are
// delegated to the Blog protocol that the issuer frontend enforces; registered clients only
// pass as exact code requests; other targets follow settings.web.sso.legacy_token_redirect.
func classifySSORedirectTargetFor(ctx context.Context, raw, issuerOrigin string) (ssoredirect.Decision, error) {
	policy, err := ssoredirect.Load()
	if err != nil {
		// Load already returned the fail-closed policy; only report the configuration problem.
		gmw.GetLogger(ctx).Error("sso redirect policy is invalid; refusing legacy and client redirects", zap.Error(err))
	}
	decision := ssoredirect.Classify(raw, issuerOrigin, policy)
	if !decision.Allowed() {
		return decision, errors.Errorf("unsupported redirect target: %s", decision.Reason)
	}
	return decision, nil
}

// ensureSSORedirectStillAllowed re-applies the current policy to a redirect target signed earlier,
// so a ceremony started under an older policy cannot complete toward a now-refused target.
func ensureSSORedirectStillAllowed(ctx context.Context, redirectTo string) error {
	if strings.TrimSpace(redirectTo) == "" {
		return nil
	}
	if _, err := classifySSORedirectTarget(ctx, redirectTo); err != nil {
		return errors.Wrap(err, "redirect target is no longer allowed; restart sign-in")
	}
	return nil
}

// resolveRequestBaseURL resolves scheme and host from the incoming request context, believing
// X-Forwarded-Host and X-Forwarded-Proto only from a trusted proxy.
// It accepts request context and returns an origin URL without a trailing slash.
func resolveRequestBaseURL(ctx context.Context) string {
	gctx, ok := gmw.GetGinCtxFromStdCtx(ctx)
	if !ok || gctx == nil || gctx.Request == nil {
		return ""
	}

	// Forwarded host and scheme count only when a trusted proxy reported them.
	host := clientip.RequestHost(gctx.Request)
	if host == "" {
		return ""
	}

	scheme := clientip.ForwardedProto(gctx.Request)
	if scheme == "" {
		if gctx.Request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}

	return strings.ToLower(scheme) + "://" + host
}

// signGitHubOAuthState signs a GitHub OAuth state payload.
// It accepts the state payload and returns an encoded state string.
func signGitHubOAuthState(payload githubOAuthState) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", errors.Wrap(err, "marshal github oauth state")
	}

	signature, err := signGitHubOAuthBytes(raw)
	if err != nil {
		return "", errors.Wrap(err, "sign github oauth state")
	}

	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// verifyGitHubOAuthState verifies and decodes a GitHub OAuth state string.
// It accepts the encoded state and returns the decoded payload when valid.
func verifyGitHubOAuthState(encoded string) (*githubOAuthState, error) {
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 {
		return nil, errors.New("invalid github oauth state format")
	}

	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.Wrap(err, "decode github oauth state")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.Wrap(err, "decode github oauth state signature")
	}

	expected, err := signGitHubOAuthBytes(raw)
	if err != nil {
		return nil, errors.Wrap(err, "sign github oauth state for comparison")
	}
	if subtle.ConstantTimeCompare(signature, expected) != 1 {
		return nil, errors.New("invalid github oauth state signature")
	}

	var payload githubOAuthState
	if err = json.Unmarshal(raw, &payload); err != nil {
		return nil, errors.Wrap(err, "unmarshal github oauth state")
	}
	if payload.ExpiresAt < gutils.Clock.GetUTCNow().Unix() {
		return nil, errors.New("github oauth state expired")
	}
	if payload.CallbackURL == "" {
		return nil, errors.New("github oauth state callback url is empty")
	}
	if payload.RedirectTo == "" {
		return nil, errors.New("github oauth state redirect target is empty")
	}
	if strings.TrimSpace(payload.Kind) == "" {
		payload.Kind = githubOAuthStateKindLogin
	}
	if payload.Kind != githubOAuthStateKindLogin && payload.Kind != githubOAuthStateKindBind {
		return nil, errors.New("invalid github oauth state kind")
	}
	if payload.Kind == githubOAuthStateKindBind && strings.TrimSpace(payload.UserUID) == "" {
		return nil, errors.New("github oauth bind state user uid is empty")
	}

	return &payload, nil
}

// signGitHubOAuthBytes signs raw state bytes with the configured application secret.
// It accepts raw payload bytes and returns the HMAC-SHA256 signature.
func signGitHubOAuthBytes(raw []byte) ([]byte, error) {
	secret := strings.TrimSpace(gconfig.Shared.GetString("settings.secret"))
	if secret == "" {
		return nil, errors.New("settings.secret is required for github oauth state")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write(raw); err != nil {
		return nil, errors.Wrap(err, "write github oauth hmac payload")
	}
	return mac.Sum(nil), nil
}

// fetchGitHubOAuthUser exchanges an OAuth code and loads the GitHub user profile.
// It accepts context, OAuth config, and authorization code, returning the user and verified email.
func fetchGitHubOAuthUser(ctx context.Context,
	config *oauth2.Config,
	code string,
) (*githubOAuthUser, string, error) {
	token, err := config.Exchange(ctx, strings.TrimSpace(code))
	if err != nil {
		return nil, "", errors.Wrap(err, "exchange github oauth code")
	}

	httpClient := config.Client(ctx, token)
	httpClient.Timeout = githubOAuthHTTPTimeout

	user, err := fetchGitHubUser(ctx, httpClient)
	if err != nil {
		return nil, "", errors.Wrap(err, "fetch github user")
	}

	email := strings.TrimSpace(strings.ToLower(user.Email))
	if email == "" {
		email, err = fetchGitHubVerifiedEmail(ctx, httpClient)
		if err != nil {
			return nil, "", errors.Wrap(err, "fetch github verified email")
		}
	}
	if email == "" {
		return nil, "", errors.New("github account has no verified email")
	}

	return user, email, nil
}

// fetchGitHubUser loads the authenticated GitHub user.
// It accepts context and OAuth HTTP client, returning the decoded user profile.
func fetchGitHubUser(ctx context.Context, httpClient *http.Client) (*githubOAuthUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return nil, errors.Wrap(err, "create github user request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	var user githubOAuthUser
	if err = doGitHubJSONRequest(httpClient, req, &user); err != nil {
		return nil, errors.Wrap(err, "decode github user")
	}
	if user.ID == 0 {
		return nil, errors.New("github user id is empty")
	}

	return &user, nil
}

// fetchGitHubVerifiedEmail loads the primary verified GitHub email address.
// It accepts context and OAuth HTTP client, returning the selected email address.
func fetchGitHubVerifiedEmail(ctx context.Context, httpClient *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user/emails", nil)
	if err != nil {
		return "", errors.Wrap(err, "create github emails request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	var emails []githubOAuthEmail
	if err = doGitHubJSONRequest(httpClient, req, &emails); err != nil {
		return "", errors.Wrap(err, "decode github emails")
	}

	for _, email := range emails {
		if email.Primary && email.Verified && strings.TrimSpace(email.Email) != "" {
			return strings.TrimSpace(strings.ToLower(email.Email)), nil
		}
	}
	for _, email := range emails {
		if email.Verified && strings.TrimSpace(email.Email) != "" {
			return strings.TrimSpace(strings.ToLower(email.Email)), nil
		}
	}

	return "", errors.New("github verified email not found")
}

// doGitHubJSONRequest sends a GitHub API request and decodes JSON into out.
// It accepts an HTTP client, request, and output pointer, returning an error on non-2xx responses.
func doGitHubJSONRequest(httpClient *http.Client, req *http.Request, out any) error {
	if httpClient == nil {
		httpClient = githubOAuthHTTPClient
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "send github api request")
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return errors.Errorf("github api returned status %d for %s", resp.StatusCode, req.URL.Path)
	}

	if err = json.NewDecoder(resp.Body).Decode(out); err != nil {
		return errors.Wrapf(err, "decode github api response for %s", req.URL.Path)
	}

	return nil
}
