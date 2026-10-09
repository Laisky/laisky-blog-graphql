package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	ginMw "github.com/Laisky/gin-middlewares/v7"
	"github.com/Laisky/zap"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Laisky/laisky-blog-graphql/library/jwt"
)

const (
	blogSSOOrigin              = "https://blog.laisky.com"
	blogSSOClient              = "blog"
	ssoCodeTTL                 = 60 * time.Second
	ssoRequestTimeout          = 5 * time.Second
	ssoBodyLimit               = 8192
	ssoErrorField              = "error"
	ssoInvalidRequest          = "invalid_request"
	ssoInvalidGrant            = "invalid_grant"
	ssoInvalidSession          = "invalid_session"
	ssoUnavailable             = "temporarily_unavailable"
	ssoAuthorizationHeader     = "Authorization"
	ssoBearerPrefix            = "Bearer "
	ssoCodeTransportContextKey = "laisky.sso.code_transport"
)

type ssoCodeBindings struct {
	ClientID        string `json:"client_id"`
	RedirectURI     string `json:"redirect_uri"`
	State           string `json:"state"`
	Challenge       string `json:"code_challenge"`
	ChallengeMethod string `json:"code_challenge_method"`
}

type ssoCodeRecord struct {
	ssoCodeBindings
	Token string `json:"token"`
}

type ssoTokenRequest struct {
	ClientID    string `json:"client_id"`
	RedirectURI string `json:"redirect_uri"`
	Code        string `json:"code"`
	State       string `json:"state"`
	Verifier    string `json:"code_verifier"`
}

type ssoCodeHandler struct {
	db       *redis.Client
	validate func(*gin.Context, string) (time.Time, error)
}

// registerSSOCodeRoutes mounts only the opt-in Blog handoff alongside the existing API prefix.
func registerSSOCodeRoutes(engine *gin.Engine, prefix urlPrefixConfig, resolver *Resolver) {
	var handler ssoCodeHandler
	if resolver != nil && resolver.args.Rdb != nil && resolver.args.BlogSvc != nil {
		handler.db = resolver.args.Rdb.GetDB().Client
		handler.validate = func(c *gin.Context, token string) (time.Time, error) {
			expiry, err := validateSSOCodeToken(token)
			if err != nil {
				return time.Time{}, err
			}
			request := c.Request.Clone(c.Request.Context())
			request.Header = c.Request.Header.Clone()
			request.Header.Set(ssoAuthorizationHeader, ssoBearerPrefix+token)
			original := c.Request
			c.Request = request
			defer func() { c.Request = original }()
			if _, err := resolver.args.BlogSvc.ValidateAndGetUser(ginMw.Ctx(c)); err != nil {
				return time.Time{}, errors.Wrap(err, "validate active sso user")
			}
			return expiry, nil
		}
	}
	for _, path := range ssoRoutePaths(prefix, "/sso/code") {
		engine.POST(path, handler.issue)
	}
	for _, path := range ssoRoutePaths(prefix, "/sso/token") {
		engine.POST(path, handler.redeem)
	}
}

// ssoRoutePaths preserves the existing public-root proxy fallback without broad new aliases.
func ssoRoutePaths(prefix urlPrefixConfig, suffix string) []string {
	paths := []string{prefix.join(suffix)}
	if prefix.public == "" && paths[0] != suffix {
		paths = append(paths, suffix)
	}
	return paths
}

// ssoCodeTransport restricts origins and preflights only for the new scoped code endpoints.
func ssoCodeTransport(prefix urlPrefixConfig) gin.HandlerFunc {
	paths := make(map[string]bool)
	for _, path := range ssoRoutePaths(prefix, "/sso/code") {
		paths[path] = false
	}
	for _, path := range ssoRoutePaths(prefix, "/sso/token") {
		paths[path] = true
	}
	return func(c *gin.Context) {
		tokenEndpoint, matched := paths[c.Request.URL.Path]
		if !matched {
			c.Next()
			return
		}
		c.Set(ssoCodeTransportContextKey, true)
		c.Header("Cache-Control", "no-store")
		c.Header("Pragma", "no-cache")
		c.Header("Referrer-Policy", "no-referrer")
		origin := c.GetHeader("Origin")
		allowed := origin == "" || origin == blogSSOOrigin
		if !tokenEndpoint {
			allowed = origin == "" || origin == "https://sso.laisky.com"
		}
		if !allowed {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{ssoErrorField: ssoInvalidRequest})
			return
		}
		if origin != "" {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Methods", "POST")
			c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		if c.Request.Method != http.MethodPost {
			c.Header("Allow", "POST, OPTIONS")
			c.AbortWithStatusJSON(http.StatusMethodNotAllowed, gin.H{ssoErrorField: ssoInvalidRequest})
			return
		}
		c.Next()
	}
}

// validateSSOCodeToken excludes legacy JWT fallback and requires a live, UUID-bound SSO bearer.
func validateSSOCodeToken(token string) (time.Time, error) {
	claims := new(jwt.UserClaims)
	if err := jwt.ParseSSOToken(token, claims); err != nil {
		return time.Time{}, errors.Wrap(err, "verify sso bearer")
	}
	if claims.ExpiresAt == nil || !claims.ExpiresAt.Time.After(time.Now()) ||
		claims.Subject == "" || claims.Subject != claims.UID {
		return time.Time{}, errors.New("invalid sso claims")
	}
	if _, err := uuid.Parse(claims.UID); err != nil {
		return time.Time{}, errors.Wrap(err, "verify sso uid")
	}
	return claims.ExpiresAt.Time, nil
}

// ssoDigest derives a non-reversible Redis key or PKCE challenge from a public code or verifier.
func ssoDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// validSSOEntropy accepts the unpadded encoding of exactly 32 random bytes.
func validSSOEntropy(value string) bool {
	if len(value) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) == 32
}

// validSSOVerifier enforces RFC 7636's 43–128 unreserved-character verifier.
func validSSOVerifier(value string) bool {
	if len(value) < 43 || len(value) > 128 {
		return false
	}
	for _, ch := range value {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' ||
			ch >= '0' && ch <= '9' || strings.ContainsRune("-._~", ch)) {
			return false
		}
	}
	return true
}

// validSSOBindings admits only the registered Blog root callback and S256 code flow.
func validSSOBindings(binding ssoCodeBindings) bool {
	return binding.ClientID == blogSSOClient && binding.RedirectURI == blogSSOOrigin &&
		validSSOEntropy(binding.State) && validSSOEntropy(binding.Challenge) && binding.ChallengeMethod == "S256"
}

// beginSSORequest bounds socket reads and downstream work before decoding a scoped SSO request.
// Its cleanup resets the connection deadline only after the entire request body reached EOF;
// rejected partial bodies retain the deadline so net/http cannot stall while draining them.
func beginSSORequest(c *gin.Context) (func(bool) error, error) {
	controller := http.NewResponseController(c.Writer)
	if err := controller.SetReadDeadline(time.Now().Add(ssoRequestTimeout)); err != nil {
		return nil, errors.Wrap(err, "set sso request read deadline")
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), ssoRequestTimeout)
	c.Request = c.Request.WithContext(ctx)
	return func(bodyComplete bool) error {
		cancel()
		if !bodyComplete {
			return nil
		}
		if err := controller.SetReadDeadline(time.Time{}); err != nil {
			return errors.Wrap(err, "reset sso request read deadline")
		}
		return nil
	}, nil
}

// decodeSSOBody bounds the full JSON body and rejects duplicate, unknown, trailing or query-carried fields.
// Its boolean reports complete body EOF independently of JSON validity for safe deadline cleanup.
func decodeSSOBody(c *gin.Context, target any) (bool, error) {
	contentType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if c.Request.URL.RawQuery != "" || err != nil || contentType != "application/json" {
		return false, errors.New("invalid sso request")
	}
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, ssoBodyLimit))
	if err != nil {
		return false, errors.Wrap(err, "read bounded sso body")
	}
	// Tokenizing the bounded object rejects duplicate keys, including escaped keys
	// and case variants that encoding/json otherwise treats as the same struct field.
	fields := json.NewDecoder(bytes.NewReader(raw))
	first, err := fields.Token()
	if err != nil || first != json.Delim('{') {
		return true, errors.New("invalid sso json object")
	}
	seen := make(map[string]struct{})
	for fields.More() {
		key, err := fields.Token()
		if err != nil {
			return true, errors.Wrap(err, "read sso json field")
		}
		name, ok := key.(string)
		if !ok {
			return true, errors.New("invalid sso json field")
		}
		name = strings.ToLower(name)
		if _, duplicate := seen[name]; duplicate {
			return true, errors.New("duplicate sso json field")
		}
		seen[name] = struct{}{}
		var value json.RawMessage
		if err := fields.Decode(&value); err != nil {
			return true, errors.Wrap(err, "read sso json value")
		}
	}
	if _, err := fields.Token(); err != nil {
		return true, errors.Wrap(err, "read sso json end")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return true, errors.Wrap(err, "decode sso request")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return true, errors.New("trailing sso request")
	}
	return true, nil
}

// issue verifies an active SSO session and stores a random one-minute Blog code without returning its bearer.
func (h ssoCodeHandler) issue(c *gin.Context) {
	logger := ginMw.GetLogger(c).Named("sso_code.issue")
	fail := func(status int, reason string) {
		logger.Debug("sso code request rejected", zap.Int("status", status), zap.String("reason", reason))
		c.JSON(status, gin.H{ssoErrorField: reason})
	}

	cleanup, err := beginSSORequest(c)
	if err != nil {
		fail(http.StatusServiceUnavailable, ssoUnavailable)
		return
	}
	bodyComplete := false
	defer func() {
		if err := cleanup(bodyComplete); err != nil {
			logger.Debug("sso read deadline reset failed")
		}
	}()
	var bindings ssoCodeBindings
	bodyComplete, decodeErr := decodeSSOBody(c, &bindings)
	if decodeErr != nil || !validSSOBindings(bindings) {
		fail(http.StatusBadRequest, ssoInvalidRequest)
		return
	}
	header := c.GetHeader(ssoAuthorizationHeader)
	if len(c.Request.Header.Values(ssoAuthorizationHeader)) != 1 ||
		!strings.HasPrefix(header, ssoBearerPrefix) || len(header) > ssoBodyLimit {
		fail(http.StatusUnauthorized, ssoInvalidSession)
		return
	}
	if h.db == nil || h.validate == nil {
		fail(http.StatusServiceUnavailable, ssoUnavailable)
		return
	}
	ctx := c.Request.Context()
	token := strings.TrimPrefix(header, ssoBearerPrefix)
	expiry, err := h.validate(c, token)
	if err != nil {
		fail(http.StatusUnauthorized, ssoInvalidSession)
		return
	}
	ttl := min(ssoCodeTTL, time.Until(expiry))
	if ttl < time.Second {
		fail(http.StatusUnauthorized, ssoInvalidSession)
		return
	}
	record, err := json.Marshal(ssoCodeRecord{bindings, token})
	if err != nil {
		fail(http.StatusServiceUnavailable, ssoUnavailable)
		return
	}
	for range 3 {
		random := make([]byte, 32)
		if _, err := rand.Read(random); err != nil {
			break
		}
		code := base64.RawURLEncoding.EncodeToString(random)
		inserted, err := h.db.SetNX(ctx, "sso:blog:code:"+ssoDigest(code), record, ttl).Result()
		if err != nil {
			break
		}
		if !inserted {
			continue
		}
		logger.Debug("issued scoped sso code")
		c.JSON(http.StatusOK, gin.H{"code": code, "expires_in": int64(ttl.Seconds())})
		return
	}
	fail(http.StatusServiceUnavailable, ssoUnavailable)
}

// consumeSSOCode validates the exact serialized record before deleting it in the same Redis execution.
const consumeSSOCode = `local value = redis.call('GET', KEYS[1])
if not value or value ~= ARGV[1] then return 0 end
return redis.call('DEL', KEYS[1])`

// redeem validates PKCE and callback bindings before atomically consuming a code and rechecking its active session.
func (h ssoCodeHandler) redeem(c *gin.Context) {
	logger := ginMw.GetLogger(c).Named("sso_code.redeem")
	fail := func(status int, reason string) {
		logger.Debug("sso code request rejected", zap.Int("status", status), zap.String("reason", reason))
		c.JSON(status, gin.H{ssoErrorField: reason})
	}

	cleanup, err := beginSSORequest(c)
	if err != nil {
		fail(http.StatusServiceUnavailable, ssoUnavailable)
		return
	}
	bodyComplete := false
	defer func() {
		if err := cleanup(bodyComplete); err != nil {
			logger.Debug("sso read deadline reset failed")
		}
	}()
	var request ssoTokenRequest
	bodyComplete, decodeErr := decodeSSOBody(c, &request)
	if decodeErr != nil || request.ClientID != blogSSOClient ||
		request.RedirectURI != blogSSOOrigin || !validSSOEntropy(request.Code) ||
		!validSSOEntropy(request.State) || !validSSOVerifier(request.Verifier) {
		fail(http.StatusBadRequest, ssoInvalidGrant)
		return
	}
	if len(c.Request.Header.Values(ssoAuthorizationHeader)) > 1 {
		fail(http.StatusBadRequest, ssoInvalidGrant)
		return
	}
	if h.db == nil || h.validate == nil {
		fail(http.StatusServiceUnavailable, ssoUnavailable)
		return
	}
	ctx := c.Request.Context()
	key := "sso:blog:code:" + ssoDigest(request.Code)
	raw, err := h.db.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		fail(http.StatusBadRequest, ssoInvalidGrant)
		return
	}
	if err != nil {
		fail(http.StatusServiceUnavailable, ssoUnavailable)
		return
	}
	var record ssoCodeRecord
	if json.Unmarshal([]byte(raw), &record) != nil || !validSSOBindings(record.ssoCodeBindings) ||
		record.ClientID != request.ClientID || record.RedirectURI != request.RedirectURI ||
		subtle.ConstantTimeCompare([]byte(record.State), []byte(request.State)) != 1 ||
		subtle.ConstantTimeCompare([]byte(record.Challenge), []byte(ssoDigest(request.Verifier))) != 1 {
		fail(http.StatusBadRequest, ssoInvalidGrant)
		return
	}
	consumed, err := h.db.Eval(ctx, consumeSSOCode, []string{key}, raw).Int()
	if err != nil {
		fail(http.StatusServiceUnavailable, ssoUnavailable)
		return
	}
	if consumed != 1 {
		fail(http.StatusBadRequest, ssoInvalidGrant)
		return
	}
	expiry, err := h.validate(c, record.Token)
	if err != nil || !expiry.After(time.Now()) {
		fail(http.StatusBadRequest, ssoInvalidGrant)
		return
	}
	logger.Debug("redeemed scoped sso code")
	c.JSON(http.StatusOK, gin.H{"access_token": record.Token, "token_type": "Bearer", "expires_in": int64(time.Until(expiry).Seconds())})
}
