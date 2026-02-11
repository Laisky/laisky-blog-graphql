package files

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	gkms "github.com/Laisky/go-utils/v6/crypto/kms"
	"github.com/Laisky/go-utils/v6/crypto/kms/mem"
)

// CredentialProtector encrypts and decrypts credential envelopes.
type CredentialProtector struct {
	kms gkms.Interface
}

// NewCredentialProtector constructs a protector from security settings.
func NewCredentialProtector(settings SecuritySettings) (*CredentialProtector, error) {
	key := strings.TrimSpace(settings.EncryptionKey)
	if len(key) <= 16 {
		return nil, errors.New("encryption key must be longer than 16 characters")
	}
	kekID := settings.EncryptionKEKID
	if kekID == 0 {
		kekID = 1
	}

	kek := sha256.Sum256([]byte(key))
	kmsClient, err := mem.New(map[uint16][]byte{kekID: kek[:]})
	if err != nil {
		return nil, errors.Wrap(err, "init kms")
	}

	return &CredentialProtector{kms: kmsClient}, nil
}

// EncryptCredential encrypts the api key using the provided AAD.
func (p *CredentialProtector) EncryptCredential(ctx context.Context, apiKey string, aad []byte) (string, error) {
	if p == nil || p.kms == nil {
		return "", errors.New("kms is not configured")
	}
	encrypted, err := p.kms.Encrypt(ctx, []byte(apiKey), aad)
	if err != nil {
		return "", errors.Wrap(err, "encrypt credential")
	}
	payload, err := encrypted.MarshalToString()
	if err != nil {
		return "", errors.Wrap(err, "marshal encrypted credential")
	}
	return payload, nil
}

// DecryptCredential decrypts the payload using the provided AAD.
func (p *CredentialProtector) DecryptCredential(ctx context.Context, payload string, aad []byte) (string, error) {
	if p == nil || p.kms == nil {
		return "", errors.New("kms is not configured")
	}
	var encrypted gkms.EncryptedData
	if err := encrypted.UnmarshalFromString(payload); err != nil {
		return "", errors.Wrap(err, "unmarshal encrypted credential")
	}
	plaintext, err := p.kms.Decrypt(ctx, &encrypted, aad)
	if err != nil {
		return "", errors.Wrap(err, "decrypt credential")
	}
	return string(plaintext), nil
}

// CredentialReference identifies a cached credential envelope.
type CredentialReference struct {
	APIKeyHash string
	Project    string
	Path       string
	UpdatedAt  time.Time
}

// CacheKey builds the redis key for the credential envelope.
// CacheKey builds the redis key for the credential envelope.
func (c CredentialReference) CacheKey(prefix string) string {
	return fmt.Sprintf("%s:%s:%s:%s:%d", prefix, c.APIKeyHash, c.Project, c.Path, c.UpdatedAt.UnixNano())
}

// AAD builds the additional authenticated data for credential encryption.
// AAD returns additional authenticated data for credential encryption.
func (c CredentialReference) AAD() []byte {
	return []byte(fmt.Sprintf("%s|%s|%s|%d", c.APIKeyHash, c.Project, c.Path, c.UpdatedAt.UnixNano()))
}
