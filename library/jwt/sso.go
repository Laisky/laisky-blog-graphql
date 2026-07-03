package jwt

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	jwtLib "github.com/golang-jwt/jwt/v5"
)

const (
	// SSOIssuer is the issuer used by SSO JWTs.
	SSOIssuer = "laisky-sso"
	// SSOTokenTTL is the validity window for SSO JWTs.
	SSOTokenTTL = 3 * 30 * 24 * time.Hour
)

// SSOJWTMetadata describes public SSO JWT verification details.
type SSOJWTMetadata struct {
	Algorithm    string         `json:"algorithm"`
	Type         string         `json:"type"`
	Issuer       string         `json:"issuer"`
	TTLSeconds   int64          `json:"ttl_seconds"`
	PublicKeyPEM string         `json:"public_key_pem"`
	ClaimsSchema map[string]any `json:"claims_schema"`
}

// SignSSOToken signs SSO claims with the configured asymmetric private key.
// It accepts user claims and returns a compact JWT string.
func SignSSOToken(claims *UserClaims) (string, error) {
	if claims == nil {
		return "", errors.New("claims is nil")
	}

	privateKey, err := ssoPrivateKey()
	if err != nil {
		return "", errors.Wrap(err, "load sso private key")
	}

	token := jwtLib.NewWithClaims(jwtLib.SigningMethodEdDSA, claims)
	signed, err := token.SignedString(privateKey)
	if err != nil {
		return "", errors.Wrap(err, "sign sso token")
	}

	return signed, nil
}

// ParseSSOToken verifies and parses an asymmetric SSO JWT.
// It accepts a compact token string and destination claims, returning nil when valid.
func ParseSSOToken(rawToken string, claims *UserClaims) error {
	if claims == nil {
		return errors.New("claims is nil")
	}

	publicKey, err := ssoPublicKey()
	if err != nil {
		return errors.Wrap(err, "load sso public key")
	}

	token, err := jwtLib.ParseWithClaims(strings.TrimSpace(rawToken), claims, func(token *jwtLib.Token) (any, error) {
		if token.Method.Alg() != jwtLib.SigningMethodEdDSA.Alg() {
			return nil, errors.Errorf("unexpected sso jwt alg %q", token.Method.Alg())
		}
		return publicKey, nil
	}, jwtLib.WithIssuer(SSOIssuer))
	if err != nil {
		return errors.Wrap(err, "parse sso token")
	}
	if !token.Valid {
		return errors.New("sso token is invalid")
	}

	return nil
}

// SSOPublicKeyPEM returns the PEM-encoded public key for SSO JWT verification.
// It accepts no parameters and returns the public key PEM string.
func SSOPublicKeyPEM() (string, error) {
	publicKey, err := ssoPublicKey()
	if err != nil {
		return "", errors.Wrap(err, "load sso public key")
	}

	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return "", errors.Wrap(err, "marshal sso public key")
	}

	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: der,
	})), nil
}

// SSOJWTInfo returns public SSO JWT verification metadata for clients.
// It accepts no parameters and returns algorithm, public key, and claim schema details.
func SSOJWTInfo() (*SSOJWTMetadata, error) {
	publicKeyPEM, err := SSOPublicKeyPEM()
	if err != nil {
		return nil, errors.Wrap(err, "load public key pem")
	}

	return &SSOJWTMetadata{
		Algorithm:    jwtLib.SigningMethodEdDSA.Alg(),
		Type:         "JWT",
		Issuer:       SSOIssuer,
		TTLSeconds:   int64(SSOTokenTTL.Seconds()),
		PublicKeyPEM: publicKeyPEM,
		ClaimsSchema: map[string]any{
			"type":     "object",
			"required": []string{"iss", "sub", "uid", "iat", "exp", "jti", "username", "display_name"},
			"properties": map[string]any{
				"iss": map[string]any{
					"type":        "string",
					"description": "Token issuer. Expected value is laisky-sso.",
				},
				"sub": map[string]any{
					"type":        "string",
					"format":      "uuid",
					"description": "Stable external user UID.",
				},
				"uid": map[string]any{
					"type":        "string",
					"format":      "uuid",
					"description": "Stable external user UID. It must match sub.",
				},
				"iat": map[string]any{
					"type":        "integer",
					"description": "Issued-at time in Unix seconds.",
				},
				"exp": map[string]any{
					"type":        "integer",
					"description": "Expiration time in Unix seconds.",
				},
				"jti": map[string]any{
					"type":        "string",
					"format":      "uuid",
					"description": "Token identifier.",
				},
				"username": map[string]any{
					"type":        "string",
					"description": "User account identifier.",
				},
				"display_name": map[string]any{
					"type":        "string",
					"description": "User display name.",
				},
			},
		},
	}, nil
}

// ssoPrivateKey loads the configured Ed25519 private key or derives one from settings.secret.
// It accepts no parameters and returns the signing private key.
func ssoPrivateKey() (ed25519.PrivateKey, error) {
	configured := strings.TrimSpace(gconfig.Shared.GetString("settings.web.sso_jwt.private_key"))
	if configured != "" {
		key, err := parseEd25519PrivateKeyPEM(configured)
		if err != nil {
			return nil, errors.Wrap(err, "parse configured sso private key")
		}
		return key, nil
	}

	secret := strings.TrimSpace(gconfig.Shared.GetString("settings.secret"))
	if secret == "" {
		return nil, errors.New("settings.secret is required for derived sso private key")
	}
	sum := sha256.Sum256([]byte(secret))
	return ed25519.NewKeyFromSeed(sum[:]), nil
}

// ssoPublicKey returns the public key corresponding to the SSO signing key.
// It accepts no parameters and returns the Ed25519 public key.
func ssoPublicKey() (ed25519.PublicKey, error) {
	privateKey, err := ssoPrivateKey()
	if err != nil {
		return nil, errors.Wrap(err, "load sso private key")
	}

	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("sso public key is not ed25519")
	}

	return publicKey, nil
}

// parseEd25519PrivateKeyPEM parses a PEM-encoded PKCS#8 Ed25519 private key.
// It accepts PEM text and returns the Ed25519 private key.
func parseEd25519PrivateKeyPEM(rawPEM string) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode([]byte(rawPEM))
	if block == nil {
		return nil, errors.New("private key pem is invalid")
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.Wrap(err, "parse pkcs8 private key")
	}

	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not ed25519")
	}

	return privateKey, nil
}
