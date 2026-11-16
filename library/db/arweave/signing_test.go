package arweave

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	errors "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// writeTestWallet writes a throwaway RSA JWK wallet and returns its path.
func writeTestWallet(t *testing.T) (string, *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	key.Precompute()

	enc := func(i *big.Int) string { return base64.RawURLEncoding.EncodeToString(i.Bytes()) }
	raw, err := json.Marshal(map[string]string{
		"kty": "RSA",
		"n":   enc(key.N),
		"e":   enc(big.NewInt(int64(key.E))),
		"d":   enc(key.D),
		"p":   enc(key.Primes[0]),
		"q":   enc(key.Primes[1]),
		"dp":  enc(key.Precomputed.Dp),
		"dq":  enc(key.Precomputed.Dq),
		"qi":  enc(key.Precomputed.Qinv),
	})
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "wallet.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	return path, &key.PublicKey
}

// verifyArweaveSignature checks a format-2 transaction against the Arweave
// signing rules: RSA-PSS/SHA-256 over the deep hash of the signed fields, and
// an ID equal to SHA-256 of the signature.
func verifyArweaveSignature(t *testing.T, tx gatewayTx, pub *rsa.PublicKey) error {
	t.Helper()
	owner := decodeB64URL(t, tx.Owner)
	if !bytes.Equal(pub.N.Bytes(), owner) {
		return errors.New("owner is not the wallet public key")
	}

	tags := make([]any, 0, len(tx.Tags))
	for _, tag := range tx.Tags {
		tags = append(tags, []any{decodeB64URL(t, tag.Name), decodeB64URL(t, tag.Value)})
	}
	signatureData := arweaveDeepHash(t, []any{
		[]byte(strconv.Itoa(tx.Format)),
		owner,
		decodeB64URL(t, tx.Target),
		[]byte(tx.Quantity),
		[]byte(tx.Reward),
		decodeB64URL(t, tx.LastTx),
		tags,
		[]byte(tx.DataSize),
		decodeB64URL(t, tx.DataRoot),
	})

	signature := decodeB64URL(t, tx.Signature)
	digest := sha256.Sum256(signatureData)
	if err := rsa.VerifyPSS(pub, crypto.SHA256, digest[:], signature,
		&rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthAuto}); err != nil {
		return errors.Wrap(err, "verify transaction signature")
	}

	id := sha256.Sum256(signature)
	if base64.RawURLEncoding.EncodeToString(id[:]) != tx.ID {
		return errors.New("transaction ID is not SHA-256 of the signature")
	}
	return nil
}

// arweaveDeepHash is an independent implementation of the Arweave deep hash.
func arweaveDeepHash(t *testing.T, item any) []byte {
	t.Helper()
	switch v := item.(type) {
	case []byte:
		tag := sha512.Sum384([]byte("blob" + strconv.Itoa(len(v))))
		data := sha512.Sum384(v)
		sum := sha512.Sum384(append(tag[:], data[:]...))
		return sum[:]
	case []any:
		acc := sha512.Sum384([]byte("list" + strconv.Itoa(len(v))))
		out := acc[:]
		for _, child := range v {
			next := sha512.Sum384(append(append([]byte{}, out...), arweaveDeepHash(t, child)...))
			out = next[:]
		}
		return out
	default:
		require.FailNowf(t, "unsupported deep-hash item", "%T", item)
		return nil
	}
}

func decodeTags(t *testing.T, tx gatewayTx) [][2]string {
	t.Helper()
	tags := make([][2]string, 0, len(tx.Tags))
	for _, tag := range tx.Tags {
		tags = append(tags, [2]string{string(decodeB64URL(t, tag.Name)), string(decodeB64URL(t, tag.Value))})
	}
	return tags
}

func decodeB64URL(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	require.NoError(t, err)
	return b
}
