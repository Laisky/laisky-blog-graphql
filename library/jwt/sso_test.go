package jwt

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	gconfig "github.com/Laisky/go-config/v2"
	gutils "github.com/Laisky/go-utils/v6"
	jwtLib "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

// TestSignAndParseSSOToken verifies EdDSA SSO token signing and public-key validation.
// It accepts the testing handle and returns by failing the test on invalid token behavior.
func TestSignAndParseSSOToken(t *testing.T) {
	originalSecret := gconfig.Shared.GetString("settings.secret")
	originalPrivateKey := gconfig.Shared.GetString("settings.web.sso_jwt.private_key")
	t.Cleanup(func() {
		gconfig.Shared.Set("settings.secret", originalSecret)
		gconfig.Shared.Set("settings.web.sso_jwt.private_key", originalPrivateKey)
	})

	gconfig.Shared.Set("settings.secret", "sso-jwt-test-secret")
	gconfig.Shared.Set("settings.web.sso_jwt.private_key", "")

	uid := gutils.UUID7()
	claims := &UserClaims{
		RegisteredClaims: jwtLib.RegisteredClaims{
			ID:        gutils.UUID7(),
			Subject:   uid,
			Issuer:    SSOIssuer,
			IssuedAt:  jwtLib.NewNumericDate(time.Now().UTC()),
			ExpiresAt: jwtLib.NewNumericDate(time.Now().UTC().Add(time.Hour)),
		},
		Username:    "alice@example.com",
		DisplayName: "Alice",
		UID:         uid,
	}

	token, err := SignSSOToken(claims)
	require.NoError(t, err)
	require.Len(t, strings.Split(token, "."), 3)

	parsed := &UserClaims{}
	require.NoError(t, ParseSSOToken(token, parsed))
	require.Equal(t, uid, parsed.Subject)
	require.Equal(t, uid, parsed.UID)
	require.Equal(t, "alice@example.com", parsed.Username)
	require.Equal(t, "Alice", parsed.DisplayName)
}

// TestSignAndParseSSOTokenWithConfiguredPrivateKey verifies the explicit PKCS#8 key configuration path.
// It accepts the testing handle and returns by failing the test when configured-key signing is broken.
func TestSignAndParseSSOTokenWithConfiguredPrivateKey(t *testing.T) {
	originalSecret := gconfig.Shared.GetString("settings.secret")
	originalPrivateKey := gconfig.Shared.GetString("settings.web.sso_jwt.private_key")
	t.Cleanup(func() {
		gconfig.Shared.Set("settings.secret", originalSecret)
		gconfig.Shared.Set("settings.web.sso_jwt.private_key", originalPrivateKey)
	})

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	privateKeyPEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: der,
	}))

	gconfig.Shared.Set("settings.secret", "")
	gconfig.Shared.Set("settings.web.sso_jwt.private_key", privateKeyPEM)

	uid := gutils.UUID7()
	token, err := SignSSOToken(&UserClaims{
		RegisteredClaims: jwtLib.RegisteredClaims{
			ID:        gutils.UUID7(),
			Subject:   uid,
			Issuer:    SSOIssuer,
			IssuedAt:  jwtLib.NewNumericDate(time.Now().UTC()),
			ExpiresAt: jwtLib.NewNumericDate(time.Now().UTC().Add(time.Hour)),
		},
		Username:    "bob@example.com",
		DisplayName: "Bob",
		UID:         uid,
	})
	require.NoError(t, err)

	parsed := &UserClaims{}
	require.NoError(t, ParseSSOToken(token, parsed))
	require.Equal(t, uid, parsed.UID)
	require.Equal(t, "bob@example.com", parsed.Username)
}

// TestSSOJWTInfo verifies that runtime metadata exposes public EdDSA verification details.
// It accepts the testing handle and returns by failing the test when metadata is incomplete.
func TestSSOJWTInfo(t *testing.T) {
	originalSecret := gconfig.Shared.GetString("settings.secret")
	originalPrivateKey := gconfig.Shared.GetString("settings.web.sso_jwt.private_key")
	t.Cleanup(func() {
		gconfig.Shared.Set("settings.secret", originalSecret)
		gconfig.Shared.Set("settings.web.sso_jwt.private_key", originalPrivateKey)
	})

	gconfig.Shared.Set("settings.secret", "sso-jwt-test-secret")
	gconfig.Shared.Set("settings.web.sso_jwt.private_key", "")

	info, err := SSOJWTInfo()
	require.NoError(t, err)
	require.Equal(t, jwtLib.SigningMethodEdDSA.Alg(), info.Algorithm)
	require.Equal(t, "JWT", info.Type)
	require.Equal(t, SSOIssuer, info.Issuer)
	require.Equal(t, int64(SSOTokenTTL.Seconds()), info.TTLSeconds)
	require.Contains(t, info.PublicKeyPEM, "BEGIN PUBLIC KEY")
	require.Contains(t, info.ClaimsSchema["required"], "uid")
}
