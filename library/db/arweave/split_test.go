package arweave

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"

	"github.com/permadao/goar/schema"
	"github.com/permadao/goar/utils"
	"github.com/stretchr/testify/require"
)

// splitTestSizes are payload sizes around every split boundary, where an odd
// balanced remainder makes goar's split differ from arweave-js's.
func splitTestSizes() []int {
	sizes := []int{1, 2, 1000}
	for _, edge := range []int{
		arweaveMinChunkSize,
		arweaveMaxChunkSize, arweaveMaxChunkSize + arweaveMinChunkSize,
		2 * arweaveMaxChunkSize, 2*arweaveMaxChunkSize + arweaveMinChunkSize,
		3 * arweaveMaxChunkSize,
	} {
		sizes = append(sizes, edge-2, edge-1, edge, edge+1, edge+2)
	}
	rng := rand.New(rand.NewPCG(20261116, 101))
	for range 6 {
		sizes = append(sizes, 1+rng.IntN(4*arweaveMaxChunkSize))
	}
	return sizes
}

func patternedPayload(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i*31 + i>>9)
	}
	return data
}

// TestGoarChunkSplitPassesNodeRules is the contract the review asked for: for
// sizes around every boundary, including 262,145 bytes, each chunk goar
// prepares is proven by its data_path and passes the node's strict and
// rebase split rules, and the chunks cover the payload exactly.
func TestGoarChunkSplitPassesNodeRules(t *testing.T) {
	differs := 0
	for _, size := range splitTestSizes() {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			data := patternedPayload(size)
			tx := &schema.Transaction{DataSize: strconv.Itoa(size)}
			require.NoError(t, utils.PrepareChunks(tx, data, size))

			cursor := 0
			var ranges [][2]int
			for i := range tx.Chunks.Chunks {
				post, err := utils.GetChunk(*tx, i, data)
				require.NoError(t, err)
				accepted, reason, detail := validateChunk(gatewayChunk{
					DataRoot: post.DataRoot, DataSize: post.DataSize, DataPath: post.DataPath, Offset: post.Offset, Chunk: post.Chunk,
				})
				require.Empty(t, reason, "chunk %d: %s", i, detail)
				require.Equal(t, cursor, accepted.start, "chunk %d must follow the previous one", i)
				cursor = accepted.end
				ranges = append(ranges, [2]int{accepted.start, accepted.end})
			}
			require.Equal(t, size, cursor, "the chunks must cover the payload")
			if !slices.Equal(ranges, canonicalChunkRanges(size)) {
				differs++
			}
		})
	}
	require.Positive(t, differs, "the sizes must include ones where goar and arweave-js split differently")
}

// TestFakeGatewayEnforcesNodeSplitRules is the negative control for the split
// check: any split point inside the balanced last stretch is valid, as are
// the node's own and arweave-js's splits, but a chunk that leaves the 256 KiB
// grid is rejected even though its Merkle proof is valid.
func TestFakeGatewayEnforcesNodeSplitRules(t *testing.T) {
	const kib = 1 << 10
	for _, tc := range []struct {
		name string
		size int
		ends []int
		// rejected lists the chunks the node rules reject.
		rejected []int
	}{
		{name: "arweave-js balances the larger half first", size: 256*kib + 1, ends: []int{131073, 256*kib + 1}},
		{name: "goar balances the larger half last", size: 256*kib + 1, ends: []int{131072, 256*kib + 1}},
		{name: "the node's own split", size: 256*kib + 1, ends: []int{256 * kib, 256*kib + 1}},
		{name: "any split point in the last stretch", size: 520 * kib, ends: []int{256 * kib, 300 * kib, 520 * kib}},
		{
			name: "a short chunk with over 512 KiB left", size: 600 * kib, ends: []int{100 * kib, 356 * kib, 600 * kib},
			rejected: []int{0, 1},
		},
		{
			name: "a short chunk followed by one inside the same bucket", size: 600 * kib, ends: []int{256 * kib, 512 * kib, 550 * kib, 600 * kib},
			rejected: []int{2, 3},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts := buildSplitChunks(patternedPayload(tc.size), tc.ends)
			for i, post := range posts {
				_, reason, detail := validateChunk(post)
				if slices.Contains(tc.rejected, i) {
					require.Equal(t, "invalid_proof", reason, "chunk %d", i)
					require.Contains(t, detail, "split rule", "chunk %d must fail the split rule, not the proof", i)
					continue
				}
				require.Empty(t, reason, "chunk %d: %s", i, detail)
			}
		})
	}
}

// TestNodeSplitRules pins the rule table against cases worked by hand from
// ar_merkle.erl.
func TestNodeSplitRules(t *testing.T) {
	const kib = 1 << 10
	for _, tc := range []struct {
		name           string
		leaf           provenLeaf
		size           int
		strict, rebase bool
	}{
		{"only chunk", provenLeaf{start: 0, end: 1000}, 1000, true, true},
		{"aligned full chunk", provenLeaf{start: 256 * kib, end: 512 * kib}, 600 * kib, true, true},
		{"misaligned full chunk", provenLeaf{start: 100 * kib, end: 356 * kib}, 600 * kib, false, false},
		{"oversized chunk", provenLeaf{start: 0, end: 256*kib + 1, rightMost: true}, 256*kib + 1, false, false},
		{"second last with over 512 KiB left", provenLeaf{start: 0, end: 100 * kib}, 600 * kib, false, true},
		{"second last balanced", provenLeaf{start: 0, end: 131072}, 256*kib + 1, true, true},
		{"last balanced", provenLeaf{start: 131072, end: 256*kib + 1, rightMost: true}, 256*kib + 1, true, true},
		{"last ending on a boundary, starting off it", provenLeaf{start: 300 * kib, end: 512 * kib, rightMost: true}, 512 * kib, false, false},
		{"last inside one bucket, off the grid", provenLeaf{start: 550 * kib, end: 600 * kib, rightMost: true}, 600 * kib, false, false},
		{"last stepping into the next bucket", provenLeaf{start: 300 * kib, end: 520 * kib, rightMost: true}, 520 * kib, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			strict, rebase := nodeSplitRules(tc.leaf, tc.size)
			require.Equal(t, tc.strict, strict, "strict")
			require.Equal(t, tc.rebase, rebase, "rebase")
		})
	}
}

// buildSplitChunks builds the /chunk bodies of a payload split at ends (each
// chunk's exclusive end offset), with the Arweave Merkle tree layout, so tests
// can offer the fake gateway splits that goar never produces.
func buildSplitChunks(data []byte, ends []int) []gatewayChunk {
	type node struct {
		id, dataHash      []byte
		start, end, split int
		left, right       *node
		isLeaf            bool
	}
	note := func(v int) []byte {
		b := make([]byte, arweaveNoteSize)
		binary.BigEndian.PutUint64(b[arweaveNoteSize-8:], uint64(v)) //nolint:gosec // test offsets are small
		return b
	}
	hash := func(parts ...[]byte) []byte {
		h := sha256.New()
		for _, p := range parts {
			_, _ = h.Write(p)
		}
		return h.Sum(nil)
	}

	var layer []*node
	start := 0
	for _, end := range ends {
		sum := sha256.Sum256(data[start:end])
		layer = append(layer, &node{
			id: hash(hash(sum[:]), hash(note(end))), dataHash: sum[:], start: start, end: end, isLeaf: true,
		})
		start = end
	}
	for len(layer) > 1 {
		var next []*node
		for i := 0; i < len(layer); i += 2 {
			if i+1 == len(layer) {
				next = append(next, layer[i])
				continue
			}
			l, r := layer[i], layer[i+1]
			next = append(next, &node{id: hash(hash(l.id), hash(r.id), hash(note(l.end))), end: r.end, split: l.end, left: l, right: r})
		}
		layer = next
	}

	root := base64.RawURLEncoding.EncodeToString(layer[0].id)
	var posts []gatewayChunk
	var walk func(n *node, path []byte)
	walk = func(n *node, path []byte) {
		if n.isLeaf {
			proof := append(append(append([]byte{}, path...), n.dataHash...), note(n.end)...)
			posts = append(posts, gatewayChunk{
				DataRoot: root, DataSize: strconv.Itoa(len(data)),
				DataPath: base64.RawURLEncoding.EncodeToString(proof),
				Offset:   strconv.Itoa(n.end - 1),
				Chunk:    base64.RawURLEncoding.EncodeToString(data[n.start:n.end]),
			})
			return
		}
		branch := append(append(append(append([]byte{}, path...), n.left.id...), n.right.id...), note(n.split)...)
		walk(n.left, branch)
		walk(n.right, branch)
	}
	walk(layer[0], nil)
	return posts
}
