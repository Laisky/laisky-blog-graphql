package oneapi

import (
	"crypto/rand"
	"math/big"
	"strings"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
)

const randomCharset = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

func randomString(length int) (string, error) {
	if length <= 0 {
		return "", errors.New("random string length must be positive")
	}
	result := make([]byte, length)
	upperBound := big.NewInt(int64(len(randomCharset)))
	for index := range result {
		value, err := rand.Int(rand.Reader, upperBound)
		if err != nil {
			return "", errors.Wrap(err, "read cryptographic randomness")
		}
		result[index] = randomCharset[value.Int64()]
	}
	return string(result), nil
}

func generateTokenKey() (string, error) {
	prefix, err := randomString(16)
	if err != nil {
		return "", err
	}
	return prefix + strings.ReplaceAll(gutils.UUID7(), "-", ""), nil
}
