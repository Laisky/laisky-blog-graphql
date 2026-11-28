package files

import (
	"context"
	"encoding/binary"

	errors "github.com/Laisky/errors/v2"
	gkms "github.com/Laisky/go-utils/v6/crypto/kms"

	kmstool "github.com/Laisky/laisky-blog-graphql/internal/library/kms"
)

// CredentialProtector encrypts and decrypts credential envelopes.
type CredentialProtector struct {
	kms gkms.Interface
}

// NewCredentialProtector constructs a protector from security settings.
func NewCredentialProtector(settings SecuritySettings) (*CredentialProtector, error) {
	kmsClient, err := kmstool.NewMemoryKMS(kmstool.Settings{KEKs: settings.KEKs()})
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

// CredentialReference identifies a stored credential envelope.
type CredentialReference struct {
	APIKeyHash string
	Project    string
	Path       string
	ID         string
}

// AAD binds the immutable envelope ID to its tenant, project and path.
func (c CredentialReference) AAD() []byte {
	aad := []byte("mcp-files-credential-v2")
	for _, part := range []string{c.ID, c.APIKeyHash, c.Project, c.Path} {
		aad = binary.AppendUvarint(aad, uint64(len(part)))
		aad = append(aad, part...)
	}
	return aad
}
