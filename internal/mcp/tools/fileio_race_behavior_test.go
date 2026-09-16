package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestFileIORaceAppend checks acknowledged payloads byte-for-byte, including
// unique records, mixed Unicode, embedded newlines, and normalized path aliases.
func TestFileIORaceAppend(t *testing.T) {
	h := newFileIORaceHarness(t, nil)
	const writers = 24
	wanted := make(map[string]bool, writers)
	operations := make([]func() fileIORaceReply, 0, writers)
	totalBytes := 0
	for i := range writers {
		record, err := json.Marshal(map[string]any{"id": i, "body": strings.Repeat("中文🙂\\\"\n", 80)})
		require.NoError(t, err)
		line := string(record)
		wanted[line] = true
		totalBytes += len(line) + 1
		operations = append(operations, func() fileIORaceReply {
			path := "/shared.jsonl"
			if i%2 == 0 {
				path = "shared.jsonl"
			}
			return h.write(i, path, line+"\n", "APPEND", 999)
		})
	}
	for _, reply := range fileIORaceTogether(operations...) {
		fileIORaceOK(t, reply)
	}
	content := h.read(t, "/shared.jsonl")
	require.Len(t, content, totalBytes)
	require.True(t, utf8.ValidString(content))
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	require.Len(t, lines, writers)
	for _, line := range lines {
		require.True(t, json.Valid([]byte(line)), "torn JSON record")
		require.True(t, wanted[line], "unknown, duplicated, or corrupt record")
		delete(wanted, line)
	}
	require.Empty(t, wanted, "acknowledged append was lost")
	h.assertStored(t, "/shared.jsonl", content)
	versions, err := h.clients[0].svc.ListVersions(h.clients[0].ctx, h.auth, h.project, "/shared.jsonl")
	require.NoError(t, err)
	require.Len(t, versions, writers-1, "each replacement must snapshot its predecessor")
	for _, v := range versions {
		snapshot, err := h.clients[0].svc.ReadVersion(h.clients[0].ctx, h.auth, h.project, "/shared.jsonl", v.ID)
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(content, string(snapshot.Content)), "history contains a torn or non-prefix append generation")
		require.Equal(t, int64(len(snapshot.Content)), snapshot.Size)
	}
}

// TestFileIORaceWholeReads accepts only entire committed documents, never an
// old/new splice. Different lengths detect retained tails and truncation tears.
func TestFileIORaceWholeReads(t *testing.T) {
	h := newFileIORaceHarness(t, nil)
	candidates := []string{`{"seed":true}`}
	for i := range 12 {
		body, err := json.Marshal(map[string]any{"generation": i, "body": strings.Repeat(fmt.Sprintf("%02d中文🙂", i), 128+i*17)})
		require.NoError(t, err)
		candidates = append(candidates, string(body))
	}
	allowed := map[string]bool{}
	for _, content := range candidates {
		allowed[content] = true
	}
	fileIORaceOK(t, h.write(0, "/atomic.json", candidates[0], "TRUNCATE", 0))
	var operations []func() fileIORaceReply
	for i := range 3 {
		operations = append(operations, func() fileIORaceReply {
			for j := range 12 {
				r := h.write(i, "/atomic.json", candidates[1+(i*4+j)%12], "TRUNCATE", 0)
				if r.err != nil || r.failed {
					return r
				}
			}
			return fileIORaceReply{}
		})
	}
	for i := range 3 {
		operations = append(operations, func() fileIORaceReply {
			for range 80 {
				r := h.call(i, "read", map[string]any{"path": "/atomic.json"})
				if r.err != nil || r.failed {
					return r
				}
				content, ok := r.payload["content"].(string)
				if !ok || !allowed[content] || !utf8.ValidString(content) || !json.Valid([]byte(content)) {
					return fileIORaceReply{failed: true, payload: map[string]any{"code": "TORN_READ", "bytes": len(content)}}
				}
			}
			return fileIORaceReply{}
		})
	}
	for _, r := range fileIORaceTogether(operations...) {
		fileIORaceOK(t, r)
	}
	final := h.read(t, "/atomic.json")
	require.True(t, allowed[final])
	h.assertStored(t, "/atomic.json", final)
}

// TestFileIORaceOverwrites distinguishes legal ordered overwrites from corruption.
func TestFileIORaceOverwrites(t *testing.T) {
	for _, tc := range []struct {
		name         string
		a, b         string
		offsetA      int
		offsetB      int
		validResults []string
	}{
		{"disjoint", "AAAA", "BBBB", 0, 8, []string{"AAAA4567BBBBcdef"}},
		{"overlapping", "AAAAAA", "BBBBBB", 2, 5, []string{"01AAABBBBBBbcdef", "01AAAAAABBBbcdef"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFileIORaceHarness(t, nil)
			fileIORaceOK(t, h.write(0, "/overwrite", "0123456789abcdef", "TRUNCATE", 0))
			for _, r := range fileIORaceTogether(
				func() fileIORaceReply { return h.write(0, "/overwrite", tc.a, "OVERWRITE", tc.offsetA) },
				func() fileIORaceReply { return h.write(1, "/overwrite", tc.b, "OVERWRITE", tc.offsetB) },
			) {
				fileIORaceOK(t, r)
			}
			final := h.read(t, "/overwrite")
			require.Contains(t, tc.validResults, final, "result is not any legal whole-operation ordering")
			h.assertStored(t, "/overwrite", final)
		})
	}
}

// TestFileIORaceNamespaceAndQuota checks path and aggregate invariants that a
// file-only mutex/CAS must not accidentally weaken.
func TestFileIORaceNamespaceAndQuota(t *testing.T) {
	t.Run("parent_vs_child", func(t *testing.T) {
		h := newFileIORaceHarness(t, nil)
		replies := fileIORaceTogether(
			func() fileIORaceReply { return h.write(0, "/parent", "parent", "TRUNCATE", 0) },
			func() fileIORaceReply { return h.write(1, "/parent/child", "child", "TRUNCATE", 0) },
		)
		successes := 0
		for _, r := range replies {
			require.NoError(t, r.err)
			if !r.failed {
				successes++
			} else {
				require.Contains(t, []string{"IS_DIRECTORY", "NOT_DIRECTORY"}, r.payload["code"])
			}
		}
		require.Equal(t, 1, successes)
	})
	t.Run("aggregate_quota", func(t *testing.T) {
		h := newFileIORaceHarness(t, func(s *files.Settings) { s.MaxProjectBytes = 12 })
		replies := fileIORaceTogether(
			func() fileIORaceReply { return h.write(0, "/a", "12345678", "TRUNCATE", 0) },
			func() fileIORaceReply { return h.write(1, "/b", "ABCDEFGH", "TRUNCATE", 0) },
		)
		successes := 0
		for _, r := range replies {
			require.NoError(t, r.err)
			if !r.failed {
				successes++
			} else {
				require.Equal(t, "QUOTA_EXCEEDED", r.payload["code"])
			}
		}
		require.Equal(t, 1, successes)
	})
}

// TestFileIORaceRenameAndDelete verifies results against both legal serial orders.
func TestFileIORaceRenameAndDelete(t *testing.T) {
	t.Run("rename_vs_append", func(t *testing.T) {
		h := newFileIORaceHarness(t, nil)
		fileIORaceOK(t, h.write(0, "/src", "seed", "TRUNCATE", 0))
		for _, r := range fileIORaceTogether(
			func() fileIORaceReply {
				return h.call(0, "rename", map[string]any{"from_path": "/src", "to_path": "/dst"})
			},
			func() fileIORaceReply { return h.write(1, "/src", "+delta", "APPEND", 0) },
		) {
			fileIORaceOK(t, r)
		}
		dst := h.read(t, "/dst")
		src := h.call(1, "read", map[string]any{"path": "/src"})
		require.NoError(t, src.err)
		if src.failed {
			require.Equal(t, "NOT_FOUND", src.payload["code"])
			require.Equal(t, "seed+delta", dst)
		} else {
			require.Equal(t, "+delta", src.payload["content"])
			require.Equal(t, "seed", dst)
		}
	})
	t.Run("delete_vs_append", func(t *testing.T) {
		h := newFileIORaceHarness(t, nil)
		fileIORaceOK(t, h.write(0, "/file", "seed", "TRUNCATE", 0))
		for _, r := range fileIORaceTogether(
			func() fileIORaceReply { return h.call(0, "delete", map[string]any{"path": "/file"}) },
			func() fileIORaceReply { return h.write(1, "/file", "+delta", "APPEND", 0) },
		) {
			fileIORaceOK(t, r)
		}
		r := h.call(2, "read", map[string]any{"path": "/file"})
		require.NoError(t, r.err)
		if r.failed {
			require.Equal(t, "NOT_FOUND", r.payload["code"])
		} else {
			require.Equal(t, "+delta", r.payload["content"], "a recreated file must not inherit deleted bytes")
		}
	})
}

// TestFileIORaceFailedWritePreservesState verifies validation failures cannot
// truncate a file, add a version, or change its metadata/hash.
func TestFileIORaceFailedWritePreservesState(t *testing.T) {
	h := newFileIORaceHarness(t, func(s *files.Settings) { s.MaxFileBytes = 16 })
	fileIORaceOK(t, h.write(0, "/stable", "seed", "TRUNCATE", 0))
	r := h.write(1, "/stable", strings.Repeat("x", 17), "TRUNCATE", 0)
	require.NoError(t, r.err)
	require.True(t, r.failed)
	h.assertStored(t, "/stable", "seed")
	versions, err := h.clients[0].svc.ListVersions(h.clients[0].ctx, h.auth, h.project, "/stable")
	require.NoError(t, err)
	require.Empty(t, versions)
}
