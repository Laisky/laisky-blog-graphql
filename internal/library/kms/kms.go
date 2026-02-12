// Package kms provides reusable internal KMS constructors.
package kms

import (
	"crypto/sha256"
	"strings"

	errors "github.com/Laisky/errors/v2"
	gkms "github.com/Laisky/go-utils/v6/crypto/kms"
	"github.com/Laisky/go-utils/v6/crypto/kms/mem"
)

// Settings describes KEK inputs for building an internal KMS instance.
type Settings struct {
	// KEKs maps KEK ID to its raw secret string.
	KEKs map[uint16]string
}

// NewMemoryKMS constructs an in-memory KMS from multiple KEKs.
//
// NewMemoryKMS hashes each KEK secret to 32 bytes and initializes
// github.com/Laisky/go-utils/v6/crypto/kms/mem with all KEK IDs.
// The mem KMS will use the KEK with the largest ID for new encryptions.
func NewMemoryKMS(settings Settings) (gkms.Interface, error) {
	if len(settings.KEKs) == 0 {
		return nil, errors.New("at least one kek is required")
	}

	hashedKEKs := make(map[uint16][]byte, len(settings.KEKs))
	for kekID, rawSecret := range settings.KEKs {
		secret := strings.TrimSpace(rawSecret)
		if len(secret) <= 16 {
			return nil, errors.Errorf("kek %d must be longer than 16 characters", kekID)
		}

		sum := sha256.Sum256([]byte(secret))
		hashedKEKs[kekID] = sum[:]
	}

	kmsClient, err := mem.New(hashedKEKs)
	if err != nil {
		return nil, errors.Wrap(err, "init memory kms")
	}

	return kmsClient, nil
}
