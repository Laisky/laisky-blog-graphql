package files_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
)

// TestFileIOCharacterization reproduces current unsafe client histories. PASS
// means the limitation was reproduced, NOT that collaborative editing is safe.
// Replace these expectations with conflict rejection when preconditions ship.
func TestFileIOCharacterization(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			t.Run("stale_read_modify_write_loses_an_independent_JSON_edit", func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				f.write(t, 0, "/state.json", `{"count":0,"label":"old"}`, files.WriteModeTruncate, 0)
				var inputs [2]string
				for _, err := range parallelRaceCalls(t, f.ctx, 2, func(i int) error {
					r, err := f.svc[i].Read(f.ctx, f.auth, "race", "/state.json", 0, -1)
					inputs[i] = r.Content
					return err
				}) {
					require.NoError(t, err)
				}
				require.Equal(t, inputs[0], inputs[1])
				var edits [2]map[string]any
				for i := range edits {
					require.NoError(t, json.Unmarshal([]byte(inputs[i]), &edits[i]))
				}
				edits[0]["count"] = float64(1)
				edits[1]["label"] = "new"
				// Deterministic history: R(A), R(B), W(A), W(B). A write-only lock
				// cannot repair a read/modify/write transaction spanning two RPCs.
				for i := range edits {
					payload, err := json.Marshal(edits[i])
					require.NoError(t, err)
					f.write(t, i, "/state.json", string(payload), files.WriteModeTruncate, 0)
				}
				var final map[string]any
				require.NoError(t, json.Unmarshal([]byte(f.read(t, 0, "/state.json")), &final))
				require.Equal(t, float64(0), final["count"], "characterization: client A's acknowledged edit is lost")
				require.Equal(t, "new", final["label"])
				versions, err := f.svc[0].ListVersions(f.ctx, f.auth, "race", "/state.json")
				require.NoError(t, err)
				require.Len(t, versions, 2)
				prior, err := f.svc[0].ReadVersion(f.ctx, f.auth, "race", "/state.json", versions[0].ID)
				require.NoError(t, err)
				require.Contains(t, string(prior.Content), `"count":1`)
				t.Log("FINDING FILEIO-001: both writes succeeded; version history permits recovery but did not prevent lost update")
			})

			t.Run("stale_byte_offset_can_corrupt_UTF8", func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				f.write(t, 0, "/text.txt", "key=0", files.WriteModeTruncate, 0)
				base := f.read(t, 1, "/text.txt")
				offset := int64(strings.Index(base, "0"))
				f.write(t, 0, "/text.txt", "key=漢0", files.WriteModeTruncate, 0)
				f.write(t, 1, "/text.txt", "1", files.WriteModeOverwrite, offset)
				got := f.read(t, 1, "/text.txt")
				expected := []byte("key=漢0")
				expected[offset] = '1'
				require.Equal(t, expected, []byte(got))
				require.False(t, utf8.ValidString(got), "characterization: valid request strings produced invalid stored UTF-8")
				f.assertStored(t, "/text.txt", got)
				t.Log("FINDING FILEIO-002: content_hash and size are internally correct even though the file bytes are invalid UTF-8")
			})

			t.Run("stale_offset_edits_the_wrong_JSON_field", func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				f.write(t, 0, "/state.json", `{"n":0}`, files.WriteModeTruncate, 0)
				base := f.read(t, 1, "/state.json")
				offset := int64(strings.Index(base, "0"))
				f.write(t, 0, "/state.json", `{"note":"new","n":0}`, files.WriteModeTruncate, 0)
				f.write(t, 1, "/state.json", "1", files.WriteModeOverwrite, offset)
				got := f.read(t, 0, "/state.json")
				require.True(t, utf8.ValidString(got))
				// The stale offset replaces a character in the new key, so this
				// particular history is syntactically valid but semantically wrong.
				require.True(t, json.Valid([]byte(got)))
				require.JSONEq(t, `{"not1":"new","n":0}`, got)
				t.Log("FINDING FILEIO-003: valid JSON is not proof of content correctness; the wrong field was edited")
			})

			t.Run("range_reads_can_assemble_a_generation_that_never_existed", func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				old, next := "AAAAAAAAaaaaaaaa", "BBBBBBBBbbbbbbbb"
				f.write(t, 0, "/parts.txt", old, files.WriteModeTruncate, 0)
				first, err := f.svc[1].Read(f.ctx, f.auth, "race", "/parts.txt", 0, 8)
				require.NoError(t, err)
				f.write(t, 0, "/parts.txt", next, files.WriteModeTruncate, 0)
				last, err := f.svc[1].Read(f.ctx, f.auth, "race", "/parts.txt", 8, -1)
				require.NoError(t, err)
				assembled := first.Content + last.Content
				require.Equal(t, "AAAAAAAAbbbbbbbb", assembled)
				require.NotContains(t, []string{old, next}, assembled)
				f.assertStored(t, "/parts.txt", next)
				t.Log("FINDING FILEIO-004: each read is atomic, but separate range reads do not share a snapshot")
			})

			t.Run("retrying_an_acknowledged_append_duplicates_content", func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				const record = "{\"request\":\"same-operation\"}\n"
				// Model a lost response by deliberately discarding the first result.
				f.write(t, 0, "/log.jsonl", record, files.WriteModeAppend, 0)
				f.write(t, 1, "/log.jsonl", record, files.WriteModeAppend, 0)
				require.Equal(t, record+record, f.read(t, 0, "/log.jsonl"))
				t.Log("FINDING FILEIO-005: APPEND is non-idempotent; a network retry needs a separate operation identity")
			})

			t.Run("stale_writer_recreates_a_deleted_or_renamed_path", func(t *testing.T) {
				for _, operation := range []string{"delete", "rename"} {
					t.Run(operation, func(t *testing.T) {
						f := newRaceFixture(t, backend, nil)
						f.write(t, 0, "/old.txt", "base", files.WriteModeTruncate, 0)
						base := f.read(t, 1, "/old.txt")
						if operation == "delete" {
							_, err := f.svc[0].Delete(f.ctx, f.auth, "race", "/old.txt", false)
							require.NoError(t, err)
						} else {
							_, err := f.svc[0].Rename(f.ctx, f.auth, "race", "/old.txt", "/new.txt", false)
							require.NoError(t, err)
						}
						f.write(t, 1, "/old.txt", base+" edited", files.WriteModeTruncate, 0)
						f.assertStored(t, "/old.txt", "base edited")
						if operation == "rename" {
							f.assertStored(t, "/new.txt", "base")
						}
						t.Log("FINDING FILEIO-006: unconditional create-on-write cannot distinguish stale editing from intentional recreation")
					})
				}
			})

			t.Run("hash_and_timestamp_do_not_identify_a_mutation_generation", func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				f.write(t, 0, "/aba.txt", "A", files.WriteModeTruncate, 0)
				before, err := f.svc[0].Stat(f.ctx, f.auth, "race", "/aba.txt")
				require.NoError(t, err)
				idBefore, contentBefore, err := f.stored("/aba.txt")
				require.NoError(t, err)
				f.write(t, 1, "/aba.txt", "B", files.WriteModeTruncate, 0)
				f.write(t, 1, "/aba.txt", "A", files.WriteModeTruncate, 0)
				after, err := f.svc[0].Stat(f.ctx, f.auth, "race", "/aba.txt")
				require.NoError(t, err)
				require.Equal(t, before.UpdatedAt, after.UpdatedAt)
				require.Equal(t, contentBefore, f.read(t, 0, "/aba.txt"))
				_, err = f.svc[1].Delete(f.ctx, f.auth, "race", "/aba.txt", false)
				require.NoError(t, err)
				f.write(t, 1, "/aba.txt", "A", files.WriteModeTruncate, 0)
				idAfter, contentAfter, err := f.stored("/aba.txt")
				require.NoError(t, err)
				require.NotEqual(t, idBefore, idAfter)
				require.Equal(t, files.HashFileContent([]byte(contentBefore)), files.HashFileContent([]byte(contentAfter)))
				t.Log("FINDING FILEIO-007: A-to-B-to-A and delete/recreate require mutation revision and incarnation semantics, not a timestamp")
			})
		})
	}
}

// TestFileIOMCPCharacterization exercises real MCP handlers and JSON encoding,
// not a mock FileService. It does not claim to test the HTTP transport/auth stack.
func TestFileIOMCPCharacterization(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			f := newRaceFixture(t, backend, nil)
			writerA, err := tools.NewFileWriteTool(f.svc[0])
			require.NoError(t, err)
			writerB, err := tools.NewFileWriteTool(f.svc[1])
			require.NoError(t, err)
			reader, err := tools.NewFileReadTool(f.svc[1])
			require.NoError(t, err)
			ctx := context.WithValue(f.ctx, ctxkeys.AuthContext, &f.auth)
			writeArgs := func(content, mode string, offset int64) map[string]any {
				return map[string]any{"project": "race", "path": "text.txt", "content": content, "mode": mode, "offset": offset}
			}
			first := callRaceTool(t, ctx, writerA, "file_write", writeArgs("key=0", "TRUNCATE", 0))
			require.Equal(t, float64(5), first["bytes_written"])
			old := callRaceTool(t, ctx, reader, "file_read", map[string]any{"project": "race", "path": "/text.txt"})
			require.Equal(t, "key=0", old["content"])
			callRaceTool(t, ctx, writerA, "file_write", writeArgs("key=漢0", "TRUNCATE", 0))
			callRaceTool(t, ctx, writerB, "file_write", writeArgs("1", "OVERWRITE", 4))
			raw := f.read(t, 0, "/text.txt")
			require.False(t, utf8.ValidString(raw))
			wire := callRaceTool(t, ctx, reader, "file_read", map[string]any{"project": "race", "path": "text.txt"})
			wireContent, ok := wire["content"].(string)
			require.True(t, ok)
			require.True(t, utf8.ValidString(wireContent))
			require.NotEqual(t, []byte(raw), []byte(wireContent))
			require.Contains(t, wireContent, "\ufffd")
			require.Equal(t, "utf-8", wire["content_encoding"])
			t.Log("FINDING FILEIO-008: successful MCP read substitutes replacement characters for invalid stored bytes")
		})
	}
}

type raceTool interface {
	Handle(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
}

func callRaceTool(t *testing.T, ctx context.Context, tool raceTool, name string, arguments map[string]any) map[string]any {
	t.Helper()
	// Decode a JSON request so numeric inputs follow the real wire representation.
	encoded, err := json.Marshal(map[string]any{"params": map[string]any{"name": name, "arguments": arguments}})
	require.NoError(t, err)
	var req mcp.CallToolRequest
	require.NoError(t, json.Unmarshal(encoded, &req))
	result, err := tool.Handle(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)
	wire, err := json.Marshal(result)
	require.NoError(t, err)
	var envelope struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(wire, &envelope))
	require.NotEmpty(t, envelope.Content)
	require.Equal(t, "text", envelope.Content[0].Type)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(envelope.Content[0].Text), &payload))
	return payload
}
