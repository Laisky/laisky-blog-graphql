//go:build fileio_conflict_audit

package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// TestFileIOConflictAudit is an intentionally strict, opt-in safety audit of
// today's API, not a claim that blind writes currently promise OCC/idempotency.
// Each failure includes a deterministic witness. Keep its CI job visibly red
// until an approved protocol addresses the gap; do not invert the assertions,
// skip the cases, or interpret the ordinary suite's green result as conflict safety.
func TestFileIOConflictAudit(t *testing.T) {
	t.Run("read_modify_write_lost_update", func(t *testing.T) {
		h := newFileIORaceHarness(t, nil)
		fileIORaceOK(t, h.write(0, "/shared.json", `{"a":0,"b":0}`, "TRUNCATE", 0))
		a := fileIORaceOK(t, h.call(0, "read", map[string]any{"path": "/shared.json"}))["content"].(string)
		b := fileIORaceOK(t, h.call(1, "read", map[string]any{"path": "/shared.json"}))["content"].(string)
		require.Equal(t, a, b, "both clients must observe the same base before either write")
		replies := fileIORaceTogether(
			func() fileIORaceReply {
				return h.write(0, "/shared.json", strings.Replace(a, `"a":0`, `"a":1`, 1), "TRUNCATE", 0)
			},
			func() fileIORaceReply {
				return h.write(1, "/shared.json", strings.Replace(b, `"b":0`, `"b":1`, 1), "TRUNCATE", 0)
			},
		)
		for _, r := range replies {
			require.NoError(t, r.err)
		}
		final := h.read(t, "/shared.json")
		var state map[string]int
		require.NoError(t, json.Unmarshal([]byte(final), &state))
		if !replies[0].failed && !replies[1].failed && (state["a"] != 1 || state["b"] != 1) {
			t.Fatalf("LOST_UPDATE: two acknowledged disjoint edits, final=%s; transaction serialization did not protect client intent", final)
		}
	})
	t.Run("stale_offset_corrupts_utf8", func(t *testing.T) {
		h := newFileIORaceHarness(t, nil)
		fileIORaceOK(t, h.write(0, "/utf8", "aXYZ", "TRUNCATE", 0))
		base := fileIORaceOK(t, h.call(0, "read", map[string]any{"path": "/utf8"}))["content"].(string)
		offset := strings.Index(base, "Y")
		require.Equal(t, 2, offset)
		// The other client inserts a multibyte character before the planned patch.
		fileIORaceOK(t, h.write(1, "/utf8", "a🙂YZ", "TRUNCATE", 0))
		r := h.write(0, "/utf8", "Q", "OVERWRITE", offset)
		require.NoError(t, r.err)
		if r.failed {
			return
		}
		raw, err := h.clients[2].svc.Read(h.clients[2].ctx, h.auth, h.project, "/utf8", 0, -1)
		require.NoError(t, err)
		wire := h.read(t, "/utf8")
		if !utf8.ValidString(raw.Content) || raw.Content != wire {
			t.Fatalf("CONTENT_CORRUPTION: accepted stale byte offset; raw_hex=%x wire=%q; JSON replacement conceals invalid stored UTF-8", raw.Content, wire)
		}
		require.Equal(t, "a🙂QZ", wire, "patch applied to wrong logical character")
	})
	t.Run("range_reads_mix_generations", func(t *testing.T) {
		h := newFileIORaceHarness(t, nil)
		fileIORaceOK(t, h.write(0, "/range", "AAAABBBB", "TRUNCATE", 0))
		first := fileIORaceOK(t, h.call(0, "read", map[string]any{"path": "/range", "offset": 0, "length": 4}))["content"].(string)
		fileIORaceOK(t, h.write(1, "/range", "CCCCDDDD", "TRUNCATE", 0))
		second := fileIORaceOK(t, h.call(0, "read", map[string]any{"path": "/range", "offset": 4, "length": 4}))["content"].(string)
		joined := first + second
		require.Contains(t, []string{"AAAABBBB", "CCCCDDDD"}, joined, "MIXED_GENERATION_READ: assembled content never existed; no version token pins range reads")
	})
	t.Run("stale_write_after_delete_recreate", func(t *testing.T) {
		h := newFileIORaceHarness(t, nil)
		fileIORaceOK(t, h.write(0, "/aba", "original", "TRUNCATE", 0))
		stale := fileIORaceOK(t, h.call(0, "read", map[string]any{"path": "/aba"}))["content"].(string)
		fileIORaceOK(t, h.call(1, "delete", map[string]any{"path": "/aba"}))
		fileIORaceOK(t, h.write(1, "/aba", "new-incarnation", "TRUNCATE", 0))
		r := h.write(0, "/aba", stale+"-edit", "TRUNCATE", 0)
		require.NoError(t, r.err)
		if !r.failed {
			t.Fatalf("STALE_INCARNATION_ACCEPTED: old reader overwrote replacement file; final=%q", h.read(t, "/aba"))
		}
	})
	t.Run("retry_duplicates_append", func(t *testing.T) {
		h := newFileIORaceHarness(t, nil)
		// First response is deliberately discarded to model a response lost AFTER
		// commit. This is not a simulated server rollback or a new logical append.
		fileIORaceOK(t, h.write(0, "/retry", "record-1\n", "APPEND", 0))
		r := h.write(1, "/retry", "record-1\n", "APPEND", 0)
		require.NoError(t, r.err)
		if !r.failed {
			require.Equal(t, "record-1\n", h.read(t, "/retry"), "DUPLICATE_RETRY: no operation identity distinguishes replay from a new append")
		}
	})
}
