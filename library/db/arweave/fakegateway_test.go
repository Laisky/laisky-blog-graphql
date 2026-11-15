package arweave

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	fakeGatewayReward = "123456"

	arweaveMaxChunkSize    = 256 << 10
	arweaveMinChunkSize    = 32 << 10
	arweaveHashSize        = 32
	arweaveNoteSize        = 32
	arweaveLeafProofSize   = arweaveHashSize + arweaveNoteSize
	arweaveBranchProofSize = 2*arweaveHashSize + arweaveNoteSize
)

// Gateway routes, keyed as "METHOD /path".
const (
	routePrice  = "GET /price"
	routeAnchor = "GET /tx_anchor"
	routeTx     = "POST /tx"
	routeChunk  = "POST /chunk"
)

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

// gatewayChunk mirrors the POST /chunk body.
type gatewayChunk struct {
	DataRoot string `json:"data_root"`
	DataSize string `json:"data_size"`
	DataPath string `json:"data_path"`
	Offset   string `json:"offset"`
	Chunk    string `json:"chunk"`
}

// acceptedChunk is a chunk whose Merkle proof the fake gateway verified.
type acceptedChunk struct {
	post       gatewayChunk
	start, end int
	data       []byte
}

// gatewayFault replaces the gateway's normal handling of a request.
type gatewayFault struct {
	status     int
	body       string
	retryAfter string
	// stall holds the request open until the client abandons it.
	stall bool
	// serveFirst lets the gateway act on the request before the fault
	// replaces its response, as when a reply is lost after acceptance.
	serveFirst bool
}

// fakeGateway implements the Arweave gateway endpoints an upload uses.
//
// Like a node, it accepts a chunk only for a posted transaction and only with
// a valid Merkle proof. It is stricter than a node in two ways: the chunk split
// must be the canonical arweave-js split (a node answers 200 to other splits
// but later drops their chunks), and the offset must be the inclusive end
// offset that arweave-js and goar send.
type fakeGateway struct {
	t       *testing.T
	url     string
	release chan struct{}

	mu        sync.Mutex
	anchor    string
	reward    string
	hits      map[string]int
	abandoned map[string]int
	faults    map[string]map[int]gatewayFault
	faultAll  map[string]gatewayFault
	hooks     map[string]func(n int)
	prices    []int
	txs       []gatewayTx
	chunks    []acceptedChunk
}

func newFakeGateway(t *testing.T) *fakeGateway {
	t.Helper()
	gw := &fakeGateway{
		t:         t,
		release:   make(chan struct{}),
		anchor:    base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 48)),
		reward:    fakeGatewayReward,
		hits:      map[string]int{},
		abandoned: map[string]int{},
		faults:    map[string]map[int]gatewayFault{},
		faultAll:  map[string]gatewayFault{},
		hooks:     map[string]func(int){},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /price/{size}", gw.handle(routePrice, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		size, err := strconv.Atoi(r.PathValue("size"))
		if err != nil {
			http.Error(w, "bad size", http.StatusBadRequest)
			return
		}
		gw.mu.Lock()
		gw.prices = append(gw.prices, size)
		reward := gw.reward
		gw.mu.Unlock()
		_, _ = w.Write([]byte(reward))
	}))
	mux.HandleFunc("GET /tx_anchor", gw.handle(routeAnchor, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		gw.mu.Lock()
		anchor := gw.anchor
		gw.mu.Unlock()
		_, _ = w.Write([]byte(anchor))
	}))
	mux.HandleFunc("POST /tx", gw.handle(routeTx, func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var tx gatewayTx
		if err := json.Unmarshal(body, &tx); err != nil {
			writeNodeError(w, "invalid_json")
			return
		}
		gw.mu.Lock()
		defer gw.mu.Unlock()
		if slices.ContainsFunc(gw.txs, func(seen gatewayTx) bool { return seen.ID == tx.ID }) {
			w.WriteHeader(http.StatusAlreadyReported)
			_, _ = w.Write([]byte("Transaction already processed."))
			return
		}
		gw.txs = append(gw.txs, tx)
		_, _ = w.Write([]byte("OK"))
	}))
	mux.HandleFunc("POST /chunk", gw.handle(routeChunk, func(w http.ResponseWriter, _ *http.Request, body []byte) {
		if reason := gw.acceptChunk(body); reason != "" {
			writeNodeError(w, reason)
		}
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected gateway request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	// Cleanups run last-in first-out: release stalled handlers before Close waits for them.
	t.Cleanup(func() { close(gw.release) })
	gw.url = srv.URL
	return gw
}

// handle counts a request, runs its hook and applies any fault before serve.
func (gw *fakeGateway) handle(route string, serve func(http.ResponseWriter, *http.Request, []byte)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}

		gw.mu.Lock()
		gw.hits[route]++
		n := gw.hits[route]
		fault, faulted := gw.faults[route][n]
		if !faulted {
			fault, faulted = gw.faultAll[route]
		}
		hook := gw.hooks[route]
		gw.mu.Unlock()

		if hook != nil {
			hook(n)
		}
		if !faulted {
			serve(w, r, body)
			return
		}
		if fault.serveFirst {
			serve(httptest.NewRecorder(), r, body)
		}
		if fault.stall {
			select {
			case <-r.Context().Done():
				gw.mu.Lock()
				gw.abandoned[route]++
				gw.mu.Unlock()
			case <-gw.release:
			}
			return
		}
		if fault.retryAfter != "" {
			w.Header().Set("Retry-After", fault.retryAfter)
		}
		w.WriteHeader(fault.status)
		_, _ = w.Write([]byte(fault.body))
	}
}

// failRequest applies fault to the nth (1-based) request on route.
func (gw *fakeGateway) failRequest(route string, n int, fault gatewayFault) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	if gw.faults[route] == nil {
		gw.faults[route] = map[int]gatewayFault{}
	}
	gw.faults[route][n] = fault
}

// failEvery applies fault to every request on route without a per-request fault.
func (gw *fakeGateway) failEvery(route string, fault gatewayFault) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	gw.faultAll[route] = fault
}

// onRequest runs hook with the request number when a request on route arrives.
func (gw *fakeGateway) onRequest(route string, hook func(n int)) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	gw.hooks[route] = hook
}

func (gw *fakeGateway) setReward(reward string) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	gw.reward = reward
}

func (gw *fakeGateway) setAnchor(anchor string) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	gw.anchor = anchor
}

func (gw *fakeGateway) hitCount(route string) int {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	return gw.hits[route]
}

func (gw *fakeGateway) totalHits() int {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	total := 0
	for _, n := range gw.hits {
		total += n
	}
	return total
}

// abandonedCount reports stalled requests on route that the client canceled.
func (gw *fakeGateway) abandonedCount(route string) int {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	return gw.abandoned[route]
}

func (gw *fakeGateway) priceRequests() []int {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	return slices.Clone(gw.prices)
}

func (gw *fakeGateway) acceptedChunks() []acceptedChunk {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	return slices.Clone(gw.chunks)
}

func (gw *fakeGateway) txCount() int {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	return len(gw.txs)
}

func (gw *fakeGateway) onlyTx(t *testing.T) gatewayTx {
	t.Helper()
	gw.mu.Lock()
	defer gw.mu.Unlock()
	require.Len(t, gw.txs, 1)
	return gw.txs[0]
}

// postChunk sends a raw /chunk body, bypassing the uploader.
func (gw *fakeGateway) postChunk(t *testing.T, chunk gatewayChunk) (int, string) {
	t.Helper()
	body, err := json.Marshal(chunk)
	require.NoError(t, err)
	resp, err := http.Post(gw.url+"/chunk", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(respBody)
}

// acceptChunk validates and stores a /chunk body, returning the node error on rejection.
func (gw *fakeGateway) acceptChunk(body []byte) string {
	var post gatewayChunk
	if err := json.Unmarshal(body, &post); err != nil {
		return "invalid_json"
	}

	gw.mu.Lock()
	known := slices.ContainsFunc(gw.txs, func(tx gatewayTx) bool {
		return tx.DataRoot == post.DataRoot && tx.DataSize == post.DataSize
	})
	gw.mu.Unlock()
	if !known {
		return "data_root_not_found"
	}

	chunk, reason, detail := validateChunk(post)
	if reason != "" {
		gw.t.Logf("fake gateway rejected chunk at offset %s: %s (%s)", post.Offset, reason, detail)
		return reason
	}

	gw.mu.Lock()
	gw.chunks = append(gw.chunks, chunk)
	gw.mu.Unlock()
	return ""
}

// deliveredData reassembles the payload the gateway accepted for tx and
// requires the verified chunks to arrive in order and cover it exactly.
func (gw *fakeGateway) deliveredData(t *testing.T, tx gatewayTx) []byte {
	t.Helper()
	if tx.Data != "" {
		return decodeB64URL(t, tx.Data)
	}
	size, err := strconv.Atoi(tx.DataSize)
	require.NoError(t, err)

	data := make([]byte, 0, size)
	for _, chunk := range gw.acceptedChunks() {
		if chunk.post.DataRoot != tx.DataRoot {
			continue
		}
		switch {
		case chunk.start == len(data):
			data = append(data, chunk.data...)
		case chunk.end <= len(data):
			require.Equal(t, data[chunk.start:chunk.end], chunk.data, "a re-posted chunk must repeat its bytes")
		default:
			require.FailNowf(t, "chunk posted out of order", "chunk [%d,%d) arrived after %d bytes", chunk.start, chunk.end, len(data))
		}
	}
	require.Len(t, data, size, "accepted chunks must cover the whole transaction")
	return data
}

// chunkRanges lists the distinct accepted chunk ranges of tx in arrival order.
func (gw *fakeGateway) chunkRanges(tx gatewayTx) [][2]int {
	var ranges [][2]int
	for _, chunk := range gw.acceptedChunks() {
		r := [2]int{chunk.start, chunk.end}
		if chunk.post.DataRoot == tx.DataRoot && !slices.Contains(ranges, r) {
			ranges = append(ranges, r)
		}
	}
	return ranges
}

func writeNodeError(w http.ResponseWriter, reason string) {
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`{"error":"` + reason + `"}`))
}

// validateChunk checks a /chunk body the way an Arweave node does, without
// goar: the data_path must lead from data_root to a leaf that commits to
// sha256(chunk) and covers offset. It returns the node's error name and a
// diagnostic detail on rejection.
func validateChunk(post gatewayChunk) (accepted acceptedChunk, reason, detail string) {
	dataSize, err := strconv.Atoi(post.DataSize)
	if err != nil || dataSize <= 0 {
		return acceptedChunk{}, "invalid_json", "data_size"
	}
	root, rootErr := base64.RawURLEncoding.DecodeString(post.DataRoot)
	chunk, chunkErr := base64.RawURLEncoding.DecodeString(post.Chunk)
	path, pathErr := base64.RawURLEncoding.DecodeString(post.DataPath)
	if rootErr != nil || chunkErr != nil || pathErr != nil {
		return acceptedChunk{}, "invalid_json", "base64url"
	}
	if len(chunk) > arweaveMaxChunkSize {
		return acceptedChunk{}, "chunk_too_big", strconv.Itoa(len(chunk))
	}
	if len(path) > arweaveMaxChunkSize {
		return acceptedChunk{}, "data_path_too_big", strconv.Itoa(len(path))
	}
	offset, err := strconv.Atoi(post.Offset)
	if err != nil {
		return acceptedChunk{}, "invalid_json", "offset"
	}
	if offset < 0 || offset >= dataSize {
		return acceptedChunk{}, "offset_too_big", post.Offset
	}

	start, end, ok := provenChunkRange(root, offset, dataSize, path, chunk)
	if !ok {
		return acceptedChunk{}, "invalid_proof", "merkle path does not prove the chunk"
	}
	if offset != end-1 {
		return acceptedChunk{}, "invalid_proof", "offset is not the chunk's inclusive end offset"
	}
	if !slices.Contains(canonicalChunkRanges(dataSize), [2]int{start, end}) {
		return acceptedChunk{}, "invalid_proof", "chunk range is not part of the canonical split"
	}
	return acceptedChunk{post: post, start: start, end: end, data: chunk}, "", ""
}

// provenChunkRange walks an Arweave data_path from root towards offset, as
// ar_merkle:validate_path does, and returns the [start, end) range of the
// leaf it reaches once that leaf is shown to commit to sha256(chunk).
func provenChunkRange(root []byte, offset, dataSize int, path, chunk []byte) (start, end int, ok bool) {
	id, left, right := root, 0, dataSize
	for len(path) > arweaveLeafProofSize {
		if len(path) < arweaveBranchProofSize {
			return 0, 0, false
		}
		leftID, rightID, note := path[:arweaveHashSize], path[arweaveHashSize:2*arweaveHashSize], path[2*arweaveHashSize:arweaveBranchProofSize]
		pivot, valid := merkleNote(note)
		if !valid || !bytes.Equal(id, merkleNodeID(leftID, rightID, note)) {
			return 0, 0, false
		}
		if offset < pivot {
			id, right = leftID, min(right, pivot)
		} else {
			id, left = rightID, max(left, pivot)
		}
		path = path[arweaveBranchProofSize:]
	}
	if len(path) != arweaveLeafProofSize {
		return 0, 0, false
	}

	dataHash, note := path[:arweaveHashSize], path[arweaveHashSize:]
	leafEnd, valid := merkleNote(note)
	if !valid || leafEnd != right || left >= right || !bytes.Equal(id, merkleNodeID(dataHash, note)) {
		return 0, 0, false
	}
	sum := sha256.Sum256(chunk)
	if !bytes.Equal(sum[:], dataHash) || len(chunk) != right-left {
		return 0, 0, false
	}
	return left, right, true
}

// merkleNodeID hashes each part and then the concatenation of those hashes.
func merkleNodeID(parts ...[]byte) []byte {
	cat := make([]byte, 0, len(parts)*sha256.Size)
	for _, part := range parts {
		sum := sha256.Sum256(part)
		cat = append(cat, sum[:]...)
	}
	sum := sha256.Sum256(cat)
	return sum[:]
}

// merkleNote decodes a 32-byte big-endian offset note.
func merkleNote(note []byte) (int, bool) {
	if len(note) != arweaveNoteSize {
		return 0, false
	}
	for _, b := range note[:arweaveNoteSize-8] {
		if b != 0 {
			return 0, false
		}
	}
	v := binary.BigEndian.Uint64(note[arweaveNoteSize-8:])
	if v > math.MaxInt {
		return 0, false
	}
	return int(v), true
}

// canonicalChunkRanges is the arweave-js chunkData split: 256 KiB chunks,
// except that a remainder below 32 KiB is balanced with the chunk before it.
// A trailing empty chunk is part of the Merkle tree but is never posted.
func canonicalChunkRanges(dataSize int) [][2]int {
	var ranges [][2]int
	cursor, rest := 0, dataSize
	for rest >= arweaveMaxChunkSize {
		size := arweaveMaxChunkSize
		if next := rest - arweaveMaxChunkSize; next > 0 && next < arweaveMinChunkSize {
			size = (rest + 1) / 2
		}
		ranges = append(ranges, [2]int{cursor, cursor + size})
		cursor, rest = cursor+size, rest-size
	}
	if rest > 0 {
		ranges = append(ranges, [2]int{cursor, cursor + rest})
	}
	return ranges
}
