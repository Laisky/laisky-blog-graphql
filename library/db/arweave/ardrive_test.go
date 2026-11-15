package arweave

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	errors "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

const fakeGatewayReward = "123456"

func TestNewArdrive_invalidWallet(t *testing.T) {
	a := NewArdrive("/nonexistent/wallet.json", "folder-id")
	require.NotNil(t, a)
	require.Nil(t, a.wallet, "wallet should be nil for invalid path")

	_, err := a.Upload(context.Background(), []byte("data"))
	require.ErrorContains(t, err, "arweave wallet not initialized")
}

// TestArdriveUploadPostsSignedDataTransaction pins the wire contract of a
// plain upload: a format-2 data transaction whose signature verifies under
// an independent implementation of the Arweave signing rules.
func TestArdriveUploadPostsSignedDataTransaction(t *testing.T) {
	walletPath, pub := writeTestWallet(t)
	gw := newFakeGateway(t)
	payload := []byte("hello arweave, 你好")

	fileID, err := newArdrive(walletPath, gw.url).Upload(context.Background(), payload)
	require.NoError(t, err)

	tx := gw.onlyTx(t)
	require.Equal(t, tx.ID, fileID)
	require.Equal(t, 2, tx.Format)
	require.Empty(t, tx.Target)
	require.Equal(t, "0", tx.Quantity)
	require.Equal(t, fakeGatewayReward, tx.Reward)
	require.Equal(t, gw.anchor, tx.LastTx)
	require.Equal(t, strconv.Itoa(len(payload)), tx.DataSize)
	require.Equal(t, []int{len(payload)}, gw.priceRequests())
	require.Equal(t, [][2]string{{"Content-Type", "text/plain"}}, decodeTags(t, tx))
	require.Equal(t, payload, gw.deliveredData(t, tx))
	require.NoError(t, verifyArweaveSignature(t, tx, pub))

	tampered := tx
	tampered.Tags = []gatewayTag{{Name: tx.Tags[0].Name, Value: base64.RawURLEncoding.EncodeToString([]byte("text/html"))}}
	require.Error(t, verifyArweaveSignature(t, tampered, pub), "a tag edit must invalidate the signature")
}

// TestArdriveUploadGzipWithContentType checks that options change the
// uploaded bytes and tags, and that pricing follows the compressed size.
func TestArdriveUploadGzipWithContentType(t *testing.T) {
	walletPath, pub := writeTestWallet(t)
	gw := newFakeGateway(t)
	payload := bytes.Repeat([]byte(`{"post":"compressible blog body"}`), 2048)

	_, err := newArdrive(walletPath, gw.url).Upload(context.Background(), payload,
		WithContentType("application/json"), WithGzip())
	require.NoError(t, err)

	tx := gw.onlyTx(t)
	delivered := gw.deliveredData(t, tx)
	require.True(t, bytes.HasPrefix(delivered, DataPrefixEnabledGz))
	require.Less(t, len(delivered), len(payload))
	decompressed, err := DecompressData(delivered)
	require.NoError(t, err)
	require.Equal(t, payload, decompressed)
	require.Equal(t, []int{len(delivered)}, gw.priceRequests())
	require.Equal(t, [][2]string{{"Content-Type", "application/json"}}, decodeTags(t, tx))
	require.NoError(t, verifyArweaveSignature(t, tx, pub))
}

// TestArdriveUploadSplitsLargePayloadIntoChunks covers payloads larger than
// one 256 KiB Arweave chunk.
func TestArdriveUploadSplitsLargePayloadIntoChunks(t *testing.T) {
	walletPath, pub := writeTestWallet(t)
	gw := newFakeGateway(t)
	payload := make([]byte, 600<<10)
	_, err := rand.Read(payload)
	require.NoError(t, err)

	_, err = newArdrive(walletPath, gw.url).Upload(context.Background(), payload)
	require.NoError(t, err)

	tx := gw.onlyTx(t)
	require.GreaterOrEqual(t, len(gw.chunkPosts()), 3)
	require.Equal(t, payload, gw.deliveredData(t, tx))
	require.NoError(t, verifyArweaveSignature(t, tx, pub))
}

// TestArdriveUploadReportsGatewayRejection ensures a rejected transaction is
// surfaced instead of returning an unconfirmed ID.
func TestArdriveUploadReportsGatewayRejection(t *testing.T) {
	walletPath, _ := writeTestWallet(t)
	gw := newFakeGateway(t)
	gw.rejectTx(http.StatusBadRequest, "Transaction verification failed.")

	fileID, err := newArdrive(walletPath, gw.url).Upload(context.Background(), []byte("rejected"))
	require.ErrorContains(t, err, "send data to arweave")
	require.Empty(t, fileID)
	require.Empty(t, gw.chunkPosts())
}

type gatewayTag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// gatewayTx mirrors the Arweave HTTP API transaction JSON independently of the SDK types.
type gatewayTx struct {
	Format    int          `json:"format"`
	ID        string       `json:"id"`
	LastTx    string       `json:"last_tx"`
	Owner     string       `json:"owner"`
	Tags      []gatewayTag `json:"tags"`
	Target    string       `json:"target"`
	Quantity  string       `json:"quantity"`
	Data      string       `json:"data"`
	DataSize  string       `json:"data_size"`
	DataRoot  string       `json:"data_root"`
	Reward    string       `json:"reward"`
	Signature string       `json:"signature"`
}

type gatewayChunk struct {
	DataRoot string `json:"data_root"`
	DataSize string `json:"data_size"`
	DataPath string `json:"data_path"`
	Offset   string `json:"offset"`
	Chunk    string `json:"chunk"`
}

// fakeGateway implements the subset of the Arweave gateway API used for uploads.
type fakeGateway struct {
	url    string
	anchor string

	mu         sync.Mutex
	prices     []int
	txs        []gatewayTx
	chunks     []gatewayChunk
	txStatus   int
	txResponse string
}

func newFakeGateway(t *testing.T) *fakeGateway {
	t.Helper()
	gw := &fakeGateway{
		anchor:     base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 48)),
		txStatus:   http.StatusOK,
		txResponse: "OK",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /price/{size}", func(w http.ResponseWriter, r *http.Request) {
		size, err := strconv.Atoi(r.PathValue("size"))
		if err != nil {
			http.Error(w, "bad size", http.StatusBadRequest)
			return
		}
		gw.mu.Lock()
		gw.prices = append(gw.prices, size)
		gw.mu.Unlock()
		_, _ = w.Write([]byte(fakeGatewayReward))
	})
	mux.HandleFunc("GET /tx_anchor", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(gw.anchor))
	})
	mux.HandleFunc("POST /tx", func(w http.ResponseWriter, r *http.Request) {
		var tx gatewayTx
		if err := json.NewDecoder(r.Body).Decode(&tx); err != nil {
			http.Error(w, "bad tx", http.StatusBadRequest)
			return
		}
		gw.mu.Lock()
		gw.txs = append(gw.txs, tx)
		status, body := gw.txStatus, gw.txResponse
		gw.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("POST /chunk", func(w http.ResponseWriter, r *http.Request) {
		var chunk gatewayChunk
		if err := json.NewDecoder(r.Body).Decode(&chunk); err != nil {
			http.Error(w, "bad chunk", http.StatusBadRequest)
			return
		}
		gw.mu.Lock()
		gw.chunks = append(gw.chunks, chunk)
		gw.mu.Unlock()
		_, _ = w.Write([]byte("OK"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected gateway request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	gw.url = srv.URL
	return gw
}

func (gw *fakeGateway) rejectTx(status int, body string) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	gw.txStatus, gw.txResponse = status, body
}

func (gw *fakeGateway) priceRequests() []int {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	return append([]int(nil), gw.prices...)
}

func (gw *fakeGateway) chunkPosts() []gatewayChunk {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	return append([]gatewayChunk(nil), gw.chunks...)
}

func (gw *fakeGateway) onlyTx(t *testing.T) gatewayTx {
	t.Helper()
	gw.mu.Lock()
	defer gw.mu.Unlock()
	require.Len(t, gw.txs, 1)
	return gw.txs[0]
}

// deliveredData returns the payload the gateway received, inline or as chunks.
func (gw *fakeGateway) deliveredData(t *testing.T, tx gatewayTx) []byte {
	t.Helper()
	if tx.Data != "" {
		return decodeB64URL(t, tx.Data)
	}

	var (
		data       []byte
		lastOffset = -1
	)
	for _, chunk := range gw.chunkPosts() {
		require.Equal(t, tx.DataRoot, chunk.DataRoot)
		require.Equal(t, tx.DataSize, chunk.DataSize)
		offset, err := strconv.Atoi(chunk.Offset)
		require.NoError(t, err)
		require.Greater(t, offset, lastOffset, "chunks must be posted in order")
		lastOffset = offset
		data = append(data, decodeB64URL(t, chunk.Chunk)...)
	}
	return data
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
