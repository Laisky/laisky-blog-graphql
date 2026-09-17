package files_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
	ragplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/rag"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
)

func readRaceVersion(t *testing.T, f *raceFixture, path string) files.ReadResult {
	t.Helper()
	r, err := f.svc[0].Read(f.ctx, f.auth, "race", path, 0, -1)
	require.NoError(t, err)
	require.NoError(t, files.ValidateFileVersion(r.Version))
	return r
}

func conditionRace(t *testing.T, f *raceFixture, path string, op files.FileOperation, p files.FilePreconditions) context.Context {
	t.Helper()
	ctx, err := files.WithFilePreconditions(f.ctx, f.auth, "race", path, op, p)
	require.NoError(t, err)
	return ctx
}

func requireVersionConflict(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	typed, ok := files.AsError(err)
	require.True(t, ok, "expected a typed conflict: %v", err)
	require.Equal(t, files.ErrCodeVersionConflict, typed.Code)
	require.False(t, typed.Retryable, "an unchanged stale request must not be automatically retried")
}

// TestFileIOVersionRegressions replaces unsafe-outcome characterization with
// conflict rejection and integrity assertions. Public mutations require conditions.
func TestFileIOVersionRegressions(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			t.Run("stale_JSON_edit_rejected_then_recomputed", func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				f.write(t, 0, "/state.json", `{"count":0,"label":"old"}`, files.WriteModeTruncate, 0)
				base := readRaceVersion(t, f, "/state.json")
				first, err := f.svc[0].WriteWith(f.ctx, f.auth, "race", "/state.json", `{"count":1,"label":"old"}`, "utf-8", 0, files.WriteModeTruncate,
					files.WriteOpts{ExpectedVersion: base.Version})
				require.NoError(t, err)
				current := readRaceVersion(t, f, "/state.json")
				require.Equal(t, first.Version, current.Version)
				versions, jobs := f.count(t, "mcp_file_versions"), f.count(t, "mcp_file_index_jobs")
				failed, err := f.svc[1].WriteWith(f.ctx, f.auth, "race", "/state.json", `{"count":0,"label":"new"}`, "utf-8", 0, files.WriteModeTruncate,
					files.WriteOpts{ExpectedVersion: base.Version})
				requireVersionConflict(t, err)
				require.Zero(t, failed)
				require.Equal(t, current, readRaceVersion(t, f, "/state.json"))
				require.Equal(t, versions, f.count(t, "mcp_file_versions"))
				require.Equal(t, jobs, f.count(t, "mcp_file_index_jobs"))
				// Recompute the intended field edit against the new bytes, never just swap tokens.
				var document map[string]any
				require.NoError(t, json.Unmarshal([]byte(current.Content), &document))
				document["label"] = "new"
				payload, err := json.Marshal(document)
				require.NoError(t, err)
				_, err = f.svc[1].WriteWith(f.ctx, f.auth, "race", "/state.json", string(payload), "utf-8", 0, files.WriteModeTruncate,
					files.WriteOpts{ExpectedVersion: current.Version})
				require.NoError(t, err)
				require.JSONEq(t, `{"count":1,"label":"new"}`, f.read(t, 0, "/state.json"))
			})

			t.Run("stale_offsets_do_not_corrupt_UTF8_or_the_wrong_JSON_field", func(t *testing.T) {
				for _, tc := range []struct {
					old, next string
					offset    int64
				}{
					{"key=0", "key=漢0", 4}, {`{"n":0}`, `{"note":"new","n":0}`, 5},
				} {
					f := newRaceFixture(t, backend, nil)
					f.write(t, 0, "/text.txt", tc.old, files.WriteModeTruncate, 0)
					base := readRaceVersion(t, f, "/text.txt")
					f.write(t, 1, "/text.txt", tc.next, files.WriteModeTruncate, 0)
					current := readRaceVersion(t, f, "/text.txt")
					versions, jobs := f.count(t, "mcp_file_versions"), f.count(t, "mcp_file_index_jobs")
					_, err := f.svc[0].WriteWith(f.ctx, f.auth, "race", "/text.txt", "1", "utf-8", tc.offset, files.WriteModeOverwrite,
						files.WriteOpts{ExpectedVersion: base.Version})
					requireVersionConflict(t, err)
					require.Equal(t, current, readRaceVersion(t, f, "/text.txt"))
					require.True(t, utf8.ValidString(current.Content))
					f.assertStored(t, "/text.txt", tc.next)
					require.Equal(t, versions, f.count(t, "mcp_file_versions"))
					require.Equal(t, jobs, f.count(t, "mcp_file_index_jobs"))
				}
			})

			t.Run("pinned_ranges_EOF_and_empty_reads_reject_changed_versions", func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				f.write(t, 0, "/parts.txt", "AAAAAAAAaaaaaaaa", files.WriteModeTruncate, 0)
				first, err := f.svc[0].Read(f.ctx, f.auth, "race", "/parts.txt", 0, 8)
				require.NoError(t, err)
				ctx := conditionRace(t, f, "/parts.txt", files.FileOperationRead, files.FilePreconditions{ExpectedVersion: first.Version})
				last, err := f.svc[1].Read(ctx, f.auth, "race", "/parts.txt", 8, -1)
				require.NoError(t, err)
				require.Equal(t, first.Version, last.Version)
				require.Equal(t, "AAAAAAAAaaaaaaaa", first.Content+last.Content)
				f.write(t, 1, "/parts.txt", "BBBBBBBBbbbbbbbb", files.WriteModeTruncate, 0)
				for _, rg := range [][2]int64{{8, -1}, {0, 0}, {99, -1}} {
					result, err := f.svc[1].Read(ctx, f.auth, "race", "/parts.txt", rg[0], rg[1])
					requireVersionConflict(t, err)
					require.Zero(t, result)
				}
				current := readRaceVersion(t, f, "/parts.txt")
				empty, err := f.svc[1].Read(f.ctx, f.auth, "race", "/parts.txt", 99, -1)
				require.NoError(t, err)
				require.Empty(t, empty.Content)
				require.Equal(t, current.Version, empty.Version)
				huge, err := f.svc[1].Read(f.ctx, f.auth, "race", "/parts.txt", 1, math.MaxInt64)
				require.NoError(t, err)
				require.Equal(t, current.Content[1:], huge.Content)
			})

			t.Run("stale_edit_cannot_recreate_a_deleted_or_renamed_path", func(t *testing.T) {
				for _, operation := range []string{"delete", "rename"} {
					t.Run(operation, func(t *testing.T) {
						f := newRaceFixture(t, backend, nil)
						f.write(t, 0, "/old.txt", "base", files.WriteModeTruncate, 0)
						base := readRaceVersion(t, f, "/old.txt")
						if operation == "delete" {
							_, err := f.svc[1].Delete(f.ctx, f.auth, "race", "/old.txt", false)
							require.NoError(t, err)
						} else {
							_, err := f.svc[1].Rename(f.ctx, f.auth, "race", "/old.txt", "/new.txt", false)
							require.NoError(t, err)
						}
						rows, versions, jobs := f.count(t, "mcp_files"), f.count(t, "mcp_file_versions"), f.count(t, "mcp_file_index_jobs")
						_, err := f.svc[0].WriteWith(f.ctx, f.auth, "race", "/old.txt", "stale edit", "utf-8", 0, files.WriteModeTruncate,
							files.WriteOpts{ExpectedVersion: base.Version})
						requireVersionConflict(t, err)
						stat, err := f.svc[0].Stat(f.ctx, f.auth, "race", "/old.txt")
						require.NoError(t, err)
						require.False(t, stat.Exists)
						require.Equal(t, rows, f.count(t, "mcp_files"))
						require.Equal(t, versions, f.count(t, "mcp_file_versions"))
						require.Equal(t, jobs, f.count(t, "mcp_file_index_jobs"))
					})
				}
			})

			t.Run("ABA_and_recreation_never_reuse_a_token", func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				f.write(t, 0, "/aba.txt", "A", files.WriteModeTruncate, 0)
				before := readRaceVersion(t, f, "/aba.txt")
				f.write(t, 1, "/aba.txt", "B", files.WriteModeTruncate, 0)
				f.write(t, 1, "/aba.txt", "A", files.WriteModeTruncate, 0)
				after := readRaceVersion(t, f, "/aba.txt")
				require.Equal(t, before.Content, after.Content)
				require.NotEqual(t, before.Version, after.Version)
				require.Equal(t, strings.Split(before.Version, ":")[0]+":3", after.Version)
				_, err := f.svc[0].WriteWith(f.ctx, f.auth, "race", "/aba.txt", "stale", "utf-8", 0, files.WriteModeTruncate, files.WriteOpts{ExpectedVersion: before.Version})
				requireVersionConflict(t, err)
				_, err = f.svc[1].Delete(f.ctx, f.auth, "race", "/aba.txt", false)
				require.NoError(t, err)
				created, err := f.svc[1].WriteWith(f.ctx, f.auth, "race", "/aba.txt", "A", "utf-8", 0, files.WriteModeTruncate, files.WriteOpts{CreateOnly: true})
				require.NoError(t, err)
				require.NotEqual(t, strings.Split(after.Version, ":")[0], strings.Split(created.Version, ":")[0])
				require.True(t, strings.HasSuffix(created.Version, ":1"))
				_, err = f.svc[0].WriteWith(f.ctx, f.auth, "race", "/aba.txt", "stale", "utf-8", 0, files.WriteModeTruncate, files.WriteOpts{ExpectedVersion: after.Version})
				requireVersionConflict(t, err)
			})

			t.Run("invalid_UTF8_and_boundaries_leave_no_side_effects", func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				f.write(t, 0, "/unicode.txt", "key=漢0", files.WriteModeTruncate, 0)
				before := readRaceVersion(t, f, "/unicode.txt")
				for _, tc := range []struct {
					content string
					offset  int64
					mode    files.WriteMode
					code    files.ErrorCode
				}{
					{"1", 4, files.WriteModeOverwrite, files.ErrCodeInvalidOffset},
					{"1", 5, files.WriteModeOverwrite, files.ErrCodeInvalidOffset},
					{string([]byte{0xff}), 0, files.WriteModeTruncate, files.ErrCodeInvalidContent},
				} {
					_, err := f.svc[1].Write(f.ctx, f.auth, "race", "/unicode.txt", tc.content, "utf-8", tc.offset, tc.mode)
					require.True(t, files.IsCode(err, tc.code), "%v", err)
					require.Equal(t, before, readRaceVersion(t, f, "/unicode.txt"))
					require.Zero(t, f.count(t, "mcp_file_versions"))
					require.Equal(t, 1, f.count(t, "mcp_file_index_jobs"))
				}
				for _, rg := range [][2]int64{{5, -1}, {4, 1}} {
					_, err := f.svc[0].Read(f.ctx, f.auth, "race", "/unicode.txt", rg[0], rg[1])
					require.True(t, files.IsCode(err, files.ErrCodeInvalidOffset), "%v", err)
				}
				// Historical corruption must fail explicitly, never be silently repaired by JSON serialization.
				invalid := []byte{0xff}
				_, err := f.db[0].ExecContext(f.ctx, f.query(`UPDATE mcp_files SET content = ?, size = ?, content_hash = ?
     WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = ?`), invalid, 1, files.HashFileContent(invalid), f.auth.APIKeyHash, "race", "/unicode.txt", "")
				require.NoError(t, err)
				_, err = f.svc[0].Read(f.ctx, f.auth, "race", "/unicode.txt", 0, -1)
				require.True(t, files.IsCode(err, files.ErrCodeInvalidContent), "%v", err)
				f.write(t, 0, "/unicode.txt", "repaired", files.WriteModeTruncate, 0)
				f.assertStored(t, "/unicode.txt", "repaired")
			})

			t.Run("same_bytes_internal_writers_precision_and_overflow", func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				f.write(t, 0, "/revision.txt", "A", files.WriteModeTruncate, 0)
				before := readRaceVersion(t, f, "/revision.txt")
				_, err := f.db[0].ExecContext(f.ctx, f.query(`UPDATE mcp_files SET content = content
     WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = ?`), f.auth.APIKeyHash, "race", "/revision.txt", "")
				require.NoError(t, err)
				after := readRaceVersion(t, f, "/revision.txt")
				require.NotEqual(t, before.Version, after.Version)
				require.Equal(t, strings.Split(before.Version, ":")[0]+":2", after.Version)
				require.NoError(t, files.RunMigrations(f.ctx, f.db[0], nil))
				require.Equal(t, after.Version, readRaceVersion(t, f, "/revision.txt").Version)
				setRevision := func(n int64) {
					_, err := f.db[0].ExecContext(f.ctx, f.query(`UPDATE mcp_files SET revision = ?
      WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = ?`), n, f.auth.APIKeyHash, "race", "/revision.txt", "")
					require.NoError(t, err)
				}
				setRevision(9007199254740993)
				high := readRaceVersion(t, f, "/revision.txt")
				require.True(t, strings.HasSuffix(high.Version, ":9007199254740993"))
				accepted, err := f.svc[0].WriteWith(f.ctx, f.auth, "race", "/revision.txt", "A", "utf-8", 0, files.WriteModeTruncate, files.WriteOpts{ExpectedVersion: high.Version})
				require.NoError(t, err)
				require.True(t, strings.HasSuffix(accepted.Version, ":9007199254740994"))
				setRevision(math.MaxInt64)
				max := readRaceVersion(t, f, "/revision.txt")
				snapshots, jobs := f.count(t, "mcp_file_versions"), f.count(t, "mcp_file_index_jobs")
				_, err = f.svc[0].WriteWith(f.ctx, f.auth, "race", "/revision.txt", "overflow", "utf-8", 0, files.WriteModeTruncate, files.WriteOpts{ExpectedVersion: max.Version})
				require.True(t, files.IsCode(err, files.ErrCodeRevisionExhausted), "%v", err)
				require.Equal(t, max, readRaceVersion(t, f, "/revision.txt"))
				require.Equal(t, snapshots, f.count(t, "mcp_file_versions"))
				require.Equal(t, jobs, f.count(t, "mcp_file_index_jobs"))
			})

			t.Run("conditional_append_retry_cannot_append_twice", func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				f.write(t, 0, "/log.txt", "", files.WriteModeTruncate, 0)
				base := readRaceVersion(t, f, "/log.txt")
				opts := files.WriteOpts{ExpectedVersion: base.Version}
				first, err := f.svc[0].WriteWith(f.ctx, f.auth, "race", "/log.txt", "record\n", "utf-8", 0, files.WriteModeAppend, opts)
				require.NoError(t, err)
				_, err = f.svc[1].WriteWith(f.ctx, f.auth, "race", "/log.txt", "record\n", "utf-8", 0, files.WriteModeAppend, opts)
				requireVersionConflict(t, err)
				current := readRaceVersion(t, f, "/log.txt")
				require.Equal(t, "record\n", current.Content)
				require.Equal(t, first.Version, current.Version)
				// CAS prevents a second effect but does not replay the original success result.
			})
		})
	}
}

// TestFileIOInternalAppendContract covers an internal imperative storage
// primitive, not an external compatibility path. MCP/HTTP omissions are rejected
// by TestFileIOClientPreconditionsMandatory and TestFileIOHTTPRequiresPreconditions.
func TestFileIOInternalAppendContract(t *testing.T) {
	f := newRaceFixture(t, "sqlite", nil)
	for range 2 {
		f.write(t, 0, "/legacy.log", "record\n", files.WriteModeAppend, 0)
	}
	require.Equal(t, "record\nrecord\n", f.read(t, 0, "/legacy.log"))
}

// TestFileIOPostgresConditionalWriters exercises actual independent connection
// pools. Same-token contenders have exactly one winner, with no losing side effects.
func TestFileIOPostgresConditionalWriters(t *testing.T) {
	for _, create := range []bool{false, true} {
		t.Run(fmt.Sprintf("create_only=%v", create), func(t *testing.T) {
			f := newRaceFixture(t, "postgres", nil)
			opts := files.WriteOpts{CreateOnly: create}
			if !create {
				f.write(t, 0, "/shared.txt", "base", files.WriteModeTruncate, 0)
				opts.ExpectedVersion = readRaceVersion(t, f, "/shared.txt").Version
			}
			const n = 24
			var results [n]files.WriteResult
			errs := parallelRaceCalls(t, f.ctx, n, func(i int) error {
				var err error
				results[i], err = f.svc[i%2].WriteWith(f.ctx, f.auth, "race", "/shared.txt", fmt.Sprintf("winner-%02d-漢", i), "utf-8", 0, files.WriteModeTruncate, opts)
				return err
			})
			winner, successes := -1, 0
			for i, err := range errs {
				if err == nil {
					winner, successes = i, successes+1
				} else {
					requireVersionConflict(t, err)
					require.Zero(t, results[i])
				}
			}
			require.Equal(t, 1, successes)
			got := readRaceVersion(t, f, "/shared.txt")
			require.Equal(t, fmt.Sprintf("winner-%02d-漢", winner), got.Content)
			require.Equal(t, results[winner].Version, got.Version)
			if create {
				require.Zero(t, f.count(t, "mcp_file_versions"))
				require.Equal(t, 1, f.count(t, "mcp_file_index_jobs"))
			} else {
				require.Equal(t, 1, f.count(t, "mcp_file_versions"))
				require.Equal(t, 2, f.count(t, "mcp_file_index_jobs"))
			}
		})
	}
	t.Run("recompute_retries_preserve_every_increment", func(t *testing.T) {
		f := newRaceFixture(t, "postgres", nil)
		f.write(t, 0, "/counter", "0", files.WriteModeTruncate, 0)
		const n = 16
		errs := parallelRaceCalls(t, f.ctx, n, func(i int) error {
			for {
				r, err := f.svc[i%2].Read(f.ctx, f.auth, "race", "/counter", 0, -1)
				if err != nil {
					return err
				}
				value, err := strconv.Atoi(r.Content)
				if err != nil {
					return err
				}
				_, err = f.svc[i%2].WriteWith(f.ctx, f.auth, "race", "/counter", strconv.Itoa(value+1), "utf-8", 0, files.WriteModeTruncate, files.WriteOpts{ExpectedVersion: r.Version})
				if files.IsCode(err, files.ErrCodeVersionConflict) {
					continue
				}
				return err
			}
		})
		for _, err := range errs {
			require.NoError(t, err)
		}
		require.Equal(t, strconv.Itoa(n), f.read(t, 0, "/counter"))
		require.Equal(t, n, f.count(t, "mcp_file_versions"))
		require.Equal(t, n+1, f.count(t, "mcp_file_index_jobs"))
	})
}

// TestFileIOMCPVersionRegression exercises real tool handlers, the manager, RAG
// adapter, JSON numeric decoding, and error payloads, not a mocked file backend.
func TestFileIOMCPVersionRegression(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			f := newRaceFixture(t, backend, nil)
			plugin, err := ragplugin.New(f.svc[0])
			require.NoError(t, err)
			manager, err := mcpplugin.NewManager("rag", plugin)
			require.NoError(t, err)
			writer, err := tools.NewFileWriteTool(manager)
			require.NoError(t, err)
			reader, err := tools.NewFileReadTool(manager)
			require.NoError(t, err)
			ctx := context.WithValue(f.ctx, ctxkeys.AuthContext, &f.auth)
			args := map[string]any{"project": "race", "path": "text.txt", "content": "key=0", "mode": "TRUNCATE", "create_only": true}
			created := callRaceTool(t, ctx, writer, "file_write", args)
			require.NotEmpty(t, created["version"])
			old := callRaceTool(t, ctx, reader, "file_read", map[string]any{"project": "race", "path": "/text.txt"})
			require.Equal(t, created["version"], old["version"])
			delete(args, "create_only")
			args["expected_version"], args["content"] = old["version"], "key=漢0"
			updated := callRaceTool(t, ctx, writer, "file_write", args)
			args["content"], args["mode"], args["offset"] = "1", "OVERWRITE", 4
			failure := callRaceToolError(t, ctx, writer, "file_write", args)
			require.Equal(t, "VERSION_CONFLICT", failure["code"])
			require.Equal(t, false, failure["retryable"])
			wire := callRaceTool(t, ctx, reader, "file_read", map[string]any{"project": "race", "path": "text.txt", "expected_version": updated["version"]})
			require.Equal(t, "key=漢0", wire["content"])
			require.Equal(t, updated["version"], wire["version"])
			require.NotContains(t, wire["content"], "\ufffd")
			for _, value := range []any{nil, "", 1, true, "bad", strings.Repeat("a", 32) + ":01"} {
				args["expected_version"] = value
				failure := callRaceToolError(t, ctx, writer, "file_write", args)
				require.Equal(t, "INVALID_ARGUMENT", failure["code"])
			}
			args["expected_version"], args["create_only"] = updated["version"], true
			require.Equal(t, "INVALID_ARGUMENT", callRaceToolError(t, ctx, writer, "file_write", args)["code"])
			f.assertStored(t, "/text.txt", "key=漢0")
		})
	}
}

type raceTool interface {
	Handle(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
}

func callRaceTool(t *testing.T, ctx context.Context, tool raceTool, name string, arguments map[string]any) map[string]any {
	t.Helper()
	return callRaceToolResult(t, ctx, tool, name, arguments, false)
}
func callRaceToolError(t *testing.T, ctx context.Context, tool raceTool, name string, arguments map[string]any) map[string]any {
	t.Helper()
	return callRaceToolResult(t, ctx, tool, name, arguments, true)
}
func callRaceToolResult(t *testing.T, ctx context.Context, tool raceTool, name string, arguments map[string]any, wantError bool) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"params": map[string]any{"name": name, "arguments": arguments}})
	require.NoError(t, err)
	var req mcp.CallToolRequest
	require.NoError(t, json.Unmarshal(encoded, &req))
	result, err := tool.Handle(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, wantError, result.IsError, "%+v", result)
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
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(envelope.Content[0].Text), &payload))
	return payload
}
