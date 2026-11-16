package arweave

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewArdrive_invalidWallet(t *testing.T) {
	a := NewArdrive("/nonexistent/wallet.json", "folder-id")
	require.NotNil(t, a)
	require.Nil(t, a.signer, "signer should be nil for invalid path")

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

	fileID, err := newTestArdrive(t, walletPath, gw.url).Upload(context.Background(), payload)
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

	_, err := newTestArdrive(t, walletPath, gw.url).Upload(context.Background(), payload,
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

// TestArdriveUploadChunksPassNodeValidation covers the chunk boundaries a node
// treats specially. Every posted chunk must carry a Merkle proof that the fake
// gateway verifies against the signed data root, at its inclusive end offset,
// and the split must pass the node's split rules.
func TestArdriveUploadChunksPassNodeValidation(t *testing.T) {
	walletPath, pub := writeTestWallet(t)
	for _, tc := range []struct {
		name   string
		size   int
		chunks [][2]int
		// goarSplit marks a layout where goar puts the larger half of a
		// balanced remainder last and arweave-js puts it first. The node
		// accepts both (see TestFakeGatewayEnforcesNodeSplitRules).
		goarSplit bool
	}{
		{name: "one byte", size: 1, chunks: [][2]int{{0, 1}}},
		{name: "exactly one chunk", size: 256 << 10, chunks: [][2]int{{0, 256 << 10}}},
		{name: "exactly two chunks", size: 512 << 10, chunks: [][2]int{{0, 256 << 10}, {256 << 10, 512 << 10}}},
		{
			name: "small remainder is balanced", size: 522 << 10,
			chunks: [][2]int{{0, 256 << 10}, {256 << 10, 389 << 10}, {389 << 10, 522 << 10}},
		},
		{
			name: "three chunks", size: 600 << 10,
			chunks: [][2]int{{0, 256 << 10}, {256 << 10, 512 << 10}, {512 << 10, 600 << 10}},
		},
		{name: "odd balanced remainder", size: 256<<10 + 1, chunks: [][2]int{{0, 131072}, {131072, 262145}}, goarSplit: true},
		{name: "largest balanced remainder", size: 288<<10 - 1, chunks: [][2]int{{0, 147455}, {147455, 294911}}, goarSplit: true},
		{name: "smallest unbalanced remainder", size: 288 << 10, chunks: [][2]int{{0, 256 << 10}, {256 << 10, 288 << 10}}},
		{
			name: "odd balanced remainder after full chunks", size: 768<<10 + 1, goarSplit: true,
			chunks: [][2]int{{0, 256 << 10}, {256 << 10, 512 << 10}, {512 << 10, 655360}, {655360, 786433}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.goarSplit {
				require.NotEqual(t, tc.chunks, canonicalChunkRanges(tc.size), "goar's split now matches arweave-js; drop goarSplit")
			} else {
				require.Equal(t, tc.chunks, canonicalChunkRanges(tc.size), "test table must match the arweave-js split")
			}
			gw := newFakeGateway(t)
			payload := make([]byte, tc.size)
			_, err := rand.Read(payload)
			require.NoError(t, err)

			fileID, err := newTestArdrive(t, walletPath, gw.url).Upload(context.Background(), payload)
			require.NoError(t, err)

			tx := gw.onlyTx(t)
			require.Equal(t, tx.ID, fileID)
			require.Equal(t, payload, gw.deliveredData(t, tx))
			require.Equal(t, tc.chunks, gw.chunkRanges(tx))
			require.Equal(t, len(tc.chunks), gw.hitCount(routeChunk), "every chunk is accepted on its first post")
			require.NoError(t, verifyArweaveSignature(t, tx, pub))
		})
	}
}

// TestFakeGatewayRejectsInvalidChunks is the negative control for the chunk
// validator: if it accepted these, the tests above could not catch a broken
// proof, offset or data root.
func TestFakeGatewayRejectsInvalidChunks(t *testing.T) {
	walletPath, _ := writeTestWallet(t)
	gw := newFakeGateway(t)
	payload := make([]byte, 600<<10)
	_, err := rand.Read(payload)
	require.NoError(t, err)
	_, err = newTestArdrive(t, walletPath, gw.url).Upload(context.Background(), payload)
	require.NoError(t, err)

	accepted := gw.acceptedChunks()
	require.Len(t, accepted, 3)
	first, middle := accepted[0].post, accepted[1].post
	flip := func(s string) string {
		b := decodeB64URL(t, s)
		b[len(b)/2] ^= 0xff
		return base64.RawURLEncoding.EncodeToString(b)
	}
	for _, tc := range []struct {
		name   string
		mutate func(c *gatewayChunk)
		want   string
	}{
		{name: "tampered chunk byte", mutate: func(c *gatewayChunk) { c.Chunk = flip(c.Chunk) }, want: "invalid_proof"},
		{name: "tampered proof byte", mutate: func(c *gatewayChunk) { c.DataPath = flip(c.DataPath) }, want: "invalid_proof"},
		{name: "proof of another chunk", mutate: func(c *gatewayChunk) { c.DataPath = first.DataPath }, want: "invalid_proof"},
		{name: "offset of another chunk", mutate: func(c *gatewayChunk) { c.Offset = first.Offset }, want: "invalid_proof"},
		{
			name: "offset inside the chunk but not its end",
			mutate: func(c *gatewayChunk) {
				c.Offset = strconv.Itoa(accepted[1].start)
			},
			want: "invalid_proof",
		},
		{name: "truncated proof", mutate: func(c *gatewayChunk) {
			b := decodeB64URL(t, c.DataPath)
			c.DataPath = base64.RawURLEncoding.EncodeToString(b[:len(b)-1])
		}, want: "invalid_proof"},
		{name: "offset past the data", mutate: func(c *gatewayChunk) { c.Offset = c.DataSize }, want: "offset_too_big"},
		{name: "oversized chunk", mutate: func(c *gatewayChunk) {
			c.Chunk = base64.RawURLEncoding.EncodeToString(make([]byte, arweaveMaxChunkSize+1))
		}, want: "chunk_too_big"},
		{name: "unknown data root", mutate: func(c *gatewayChunk) { c.DataRoot = flip(c.DataRoot) }, want: "data_root_not_found"},
		{name: "data size mismatch", mutate: func(c *gatewayChunk) { c.DataSize = "1" }, want: "data_root_not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			post := middle
			tc.mutate(&post)
			status, body := gw.postChunk(t, post)
			require.Equal(t, http.StatusBadRequest, status)
			require.JSONEq(t, `{"error":"`+tc.want+`"}`, body)
		})
	}

	status, _ := gw.postChunk(t, middle)
	require.Equal(t, http.StatusOK, status, "an unmodified chunk is accepted again")
	require.Len(t, gw.acceptedChunks(), 4, "only the unmodified replay was stored")
}

// TestArdriveUploadStopsOnFatalChunkRejection checks that a chunk the gateway
// rejects as permanently invalid fails the upload at once, names the paid
// transaction for follow-up, and sends no further chunks.
func TestArdriveUploadStopsOnFatalChunkRejection(t *testing.T) {
	walletPath, _ := writeTestWallet(t)
	gw := newFakeGateway(t)
	gw.failRequest(routeChunk, 2, gatewayFault{status: http.StatusBadRequest, body: `{"error":"invalid_proof"}`})
	payload := make([]byte, 600<<10)
	_, err := rand.Read(payload)
	require.NoError(t, err)

	fileID, err := newTestArdrive(t, walletPath, gw.url).Upload(context.Background(), payload)
	require.ErrorContains(t, err, "invalid_proof")
	require.ErrorContains(t, err, gw.onlyTx(t).ID, "the error must name the transaction whose data is incomplete")
	require.Empty(t, fileID)
	require.Equal(t, 2, gw.hitCount(routeChunk), "a fatal rejection is neither retried nor followed by later chunks")
}

// TestArdriveUploadReportsGatewayRejection ensures a rejected transaction is
// surfaced instead of returning an unconfirmed ID, without retrying a
// permanent rejection or sending chunks.
func TestArdriveUploadReportsGatewayRejection(t *testing.T) {
	walletPath, _ := writeTestWallet(t)
	gw := newFakeGateway(t)
	gw.failEvery(routeTx, gatewayFault{status: http.StatusBadRequest, body: "Transaction verification failed."})

	fileID, err := newTestArdrive(t, walletPath, gw.url).Upload(context.Background(), []byte("rejected"))
	require.ErrorContains(t, err, "send data to arweave")
	require.ErrorContains(t, err, "Transaction verification failed.")
	require.Empty(t, fileID)
	require.Equal(t, 1, gw.hitCount(routeTx))
	require.Zero(t, gw.hitCount(routeChunk))
}
