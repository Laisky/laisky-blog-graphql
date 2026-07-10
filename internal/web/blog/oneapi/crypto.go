package oneapi

import (
	"sync"

	"github.com/Laisky/errors/v2"
	gcrypto "github.com/Laisky/go-utils/v6/crypto"
)

// Password2Hash converts a plaintext password into a bcrypt hash using the
// default cost. It mirrors one-api's common.Password2Hash so hashes written by
// the SSO service are verifiable by one-api and vice versa.
func Password2Hash(password string) (string, error) {
	hashed, err := gcrypto.GeneratePasswordHash([]byte(password)) //nolint:staticcheck // OneAPI requires this bcrypt-compatible project helper.
	if err != nil {
		return "", errors.Wrap(err, "generate password hash")
	}
	return string(hashed), nil
}

// ValidatePasswordAndHash reports whether the plaintext password matches the
// supplied bcrypt hash. It mirrors one-api's common.ValidatePasswordAndHash.
func ValidatePasswordAndHash(password string, hash string) bool {
	return gcrypto.ValidatePasswordHash([]byte(hash), []byte(password)) //nolint:staticcheck // OneAPI requires bcrypt compatibility.
}

var (
	dummyPasswordHashOnce sync.Once
	dummyPasswordHashVal  string
)

// dummyPasswordHash returns a precomputed bcrypt hash used to mask login timing
// for unknown accounts, so response time does not reveal whether an account
// exists.
func dummyPasswordHash() string {
	dummyPasswordHashOnce.Do(func() {
		hashed, err := gcrypto.GeneratePasswordHash([]byte("invalid-password-placeholder")) //nolint:staticcheck // Timing mask must use OneAPI bcrypt.
		if err == nil {
			dummyPasswordHashVal = string(hashed)
		}
	})
	return dummyPasswordHashVal
}
