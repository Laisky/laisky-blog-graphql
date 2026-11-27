package files_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// renameTestBackends runs a rename scenario on both storage backends; the
// PostgreSQL run is skipped without FILEIO_TEST_POSTGRES_DSN.
var renameTestBackends = []string{"sqlite", "postgres"}

// renameTargets are the destination projects of a rename scenario: the source
// project itself, and another project of the same caller.
var renameTargets = []string{"race", "race-peer"}

// tenantSnapshot is every row of the fixture tenant that a rename can change:
// live and deleted files, history rows and index outbox jobs, in all projects.
type tenantSnapshot struct {
	files    []string
	versions []string
	jobs     []string
}

// snapshotTenant reads the fixture tenant's file, history and outbox rows.
func (f *raceFixture) snapshotTenant(t *testing.T) tenantSnapshot {
	t.Helper()
	return tenantSnapshot{
		files: f.rows(t, `SELECT id, project, path, deleted, content_hash FROM mcp_files
			WHERE apikey_hash = ? AND system_owner = ? ORDER BY id`, 5),
		versions: f.rows(t, `SELECT id, project, path, size FROM mcp_file_versions
			WHERE apikey_hash = ? AND system_owner = ? ORDER BY id`, 4),
		jobs: f.rows(t, `SELECT id, project, file_path, operation FROM mcp_file_index_jobs
			WHERE apikey_hash = ? AND system_owner = ? ORDER BY id`, 4),
	}
}

// rows renders each result row of query, bound to the fixture tenant, as text.
func (f *raceFixture) rows(t *testing.T, query string, columns int) []string {
	t.Helper()
	result, err := f.db[1].QueryContext(f.ctx, f.query(query), f.auth.APIKeyHash, "")
	require.NoError(t, err)
	defer func() { require.NoError(t, result.Close()) }()
	var out []string
	for result.Next() {
		values := make([]any, columns)
		pointers := make([]any, columns)
		for i := range values {
			pointers[i] = &values[i]
		}
		require.NoError(t, result.Scan(pointers...))
		out = append(out, fmt.Sprint(values...))
	}
	require.NoError(t, result.Err())
	return out
}

// writeFiles creates each path of project with its content.
func (f *raceFixture) writeFiles(t *testing.T, project string, contents map[string]string) {
	t.Helper()
	for path, content := range contents {
		_, err := f.svc[0].Write(f.ctx, f.auth, project, path, content, "utf-8", 0, files.WriteModeTruncate)
		require.NoError(t, err)
	}
}

// requireContents asserts every path of project holds its content.
func (f *raceFixture) requireContents(t *testing.T, project string, contents map[string]string) {
	t.Helper()
	for path, content := range contents {
		read, err := f.svc[0].Read(f.ctx, f.auth, project, path, 0, -1)
		require.NoError(t, err, "%s:%s", project, path)
		require.Equal(t, content, read.Content, "%s:%s", project, path)
	}
}

// requireRejectedRename asserts req fails with code and changes no file,
// history or outbox row of the tenant.
func (f *raceFixture) requireRejectedRename(t *testing.T, req files.RenameRequest, code files.ErrorCode) {
	t.Helper()
	before := f.snapshotTenant(t)
	_, err := f.svc[0].Rename(f.ctx, f.auth, req)
	require.Error(t, err)
	require.True(t, files.IsCode(err, code), "want %s, got %v", code, err)
	require.Equal(t, before, f.snapshotTenant(t), "a rejected rename must not change rows, history or outbox")
}

// TestRenameDirectoryRequiresVacantDestination verifies a directory is
// relocated, never merged or replaced: within a project and across projects,
// an existing destination directory (even with disjoint children), a nested
// descendant, an exact file or a file ancestor rejects the move, overwrite=true
// is refused for a directory, and nothing changes.
func TestRenameDirectoryRequiresVacantDestination(t *testing.T) {
	cases := []struct {
		name        string
		source      map[string]string
		destination map[string]string
		toPath      string
		code        files.ErrorCode
	}{
		{"existing directory with disjoint children", map[string]string{"/src/a.md": "a"}, map[string]string{"/dst/other.md": "other"}, "/dst", files.ErrCodeAlreadyExists},
		{"nested destination descendant", map[string]string{"/src/a.md": "a"}, map[string]string{"/dst/a.md/b.md": "nested"}, "/dst", files.ErrCodeAlreadyExists},
		{"destination file below a moved directory", map[string]string{"/src/a/b.md": "b"}, map[string]string{"/dst/a": "a file"}, "/dst", files.ErrCodeAlreadyExists},
		{"exact destination file", map[string]string{"/src/a.md": "a"}, map[string]string{"/dst": "a file"}, "/dst", files.ErrCodeAlreadyExists},
		{"file ancestor", map[string]string{"/src/a.md": "a"}, map[string]string{"/d": "a file"}, "/d/dst", files.ErrCodeNotDirectory},
	}
	for _, backend := range renameTestBackends {
		for _, target := range renameTargets {
			for _, tc := range cases {
				t.Run(backend+"/"+target+"/"+tc.name, func(t *testing.T) {
					f := newRaceFixture(t, backend, nil)
					f.writeFiles(t, "race", tc.source)
					f.writeFiles(t, target, tc.destination)
					req := files.RenameRequest{Project: "race", FromPath: "/src", TargetProject: target, ToPath: tc.toPath}
					f.requireRejectedRename(t, req, tc.code)
					req.Overwrite = true
					f.requireRejectedRename(t, req, files.ErrCodeInvalidArgument)
					f.requireContents(t, "race", tc.source)
					f.requireContents(t, target, tc.destination)
				})
			}
		}
	}
}

// TestRenameDirectoryRejectsOwnParentAndSubtree verifies a directory cannot
// move into its own subtree, nor into its own parent or another ancestor: the
// ancestor already holds the directory, so it is not vacant.
func TestRenameDirectoryRejectsOwnParentAndSubtree(t *testing.T) {
	for _, backend := range renameTestBackends {
		t.Run(backend, func(t *testing.T) {
			f := newRaceFixture(t, backend, nil)
			f.writeFiles(t, "race", map[string]string{"/a/b/x.md": "shallow", "/a/b/b/x.md": "deep", "/a/b/c": "file c"})
			f.requireRejectedRename(t, files.RenameRequest{Project: "race", FromPath: "/a/b", ToPath: "/a/b/c/d"}, files.ErrCodeInvalidPath)
			f.requireRejectedRename(t, files.RenameRequest{Project: "race", FromPath: "/a/b", ToPath: "/a"}, files.ErrCodeAlreadyExists)
			f.requireRejectedRename(t, files.RenameRequest{Project: "race", FromPath: "/a/b/b", ToPath: "/a"}, files.ErrCodeAlreadyExists)
			f.requireContents(t, "race", map[string]string{"/a/b/x.md": "shallow", "/a/b/b/x.md": "deep", "/a/b/c": "file c"})
		})
	}
}

// TestRenameDirectoryValidatesEveryGeneratedPath verifies a directory move is
// rejected when one moved file's new path exceeds the path length limit,
// although the source paths and the destination root are valid.
func TestRenameDirectoryValidatesEveryGeneratedPath(t *testing.T) {
	long := "/src/" + strings.Repeat("x", 300) + ".md"
	toPath := "/" + strings.Repeat("d", 250)
	require.NoError(t, files.ValidatePath(long))
	require.NoError(t, files.ValidatePath(toPath))
	require.Error(t, files.ValidatePath(toPath+strings.TrimPrefix(long, "/src")))
	for _, backend := range renameTestBackends {
		for _, target := range renameTargets {
			t.Run(backend+"/"+target, func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				f.writeFiles(t, "race", map[string]string{"/src/short.md": "short", long: "long"})
				f.requireRejectedRename(t, files.RenameRequest{Project: "race", FromPath: "/src", TargetProject: target, ToPath: toPath}, files.ErrCodeInvalidPath)
			})
		}
	}
}

// TestRenameDirectoryToVacantPath verifies a directory relocates to a vacant
// path within a project and across projects, also when the textual path is
// unchanged across projects: every file keeps its row identity and its own
// history, and only the selected subtree moves. Paths matched by an unescaped
// or case-insensitive LIKE ("_" wildcard, ASCII case folding) neither occupy
// the destination nor join the source.
func TestRenameDirectoryToVacantPath(t *testing.T) {
	for _, backend := range renameTestBackends {
		for _, target := range renameTargets {
			for _, toPath := range []string{"/d_t", "/s_c"} {
				if toPath == "/s_c" && target == "race" {
					continue // The same path in the same project is a no-op, covered below.
				}
				t.Run(backend+"/"+target+toPath, func(t *testing.T) {
					f := newRaceFixture(t, backend, nil)
					source := map[string]string{"/s_c/a.md": "a v2", "/s_c/sub/b.md": "b"}
					unrelated := map[string]string{"/sXc/z.md": "not selected", "/S_C/up.md": "other case"}
					occupants := map[string]string{"/dXt/y.md": "underscore sibling", "/D_T/y.md": "case sibling"}
					f.writeFiles(t, "race", map[string]string{"/s_c/a.md": "a v1"})
					f.writeFiles(t, "race", source)
					f.writeFiles(t, "race", unrelated)
					f.writeFiles(t, target, occupants)
					historyBefore := map[string]int{}
					idsBefore := map[string]uint64{}
					for path := range source {
						versions, err := f.svc[0].ListVersions(f.ctx, f.auth, "race", path)
						require.NoError(t, err)
						historyBefore[path] = len(versions)
						idsBefore[path] = f.fileID(t, "race", path)
					}
					require.Equal(t, 1, historyBefore["/s_c/a.md"], "the rewritten file has one prior snapshot")

					result, err := f.svc[0].Rename(f.ctx, f.auth, files.RenameRequest{Project: "race", FromPath: "/s_c", TargetProject: target, ToPath: toPath})
					require.NoError(t, err)
					require.Equal(t, len(source), result.MovedCount)
					for path, content := range source {
						moved := toPath + strings.TrimPrefix(path, "/s_c")
						f.requireContents(t, target, map[string]string{moved: content})
						versions, err := f.svc[0].ListVersions(f.ctx, f.auth, target, moved)
						require.NoError(t, err)
						require.Len(t, versions, historyBefore[path], "%s keeps its own history", moved)
						require.Equal(t, idsBefore[path], f.fileID(t, target, moved), "%s keeps its row identity", moved)
						stat, err := f.svc[0].Stat(f.ctx, f.auth, "race", path)
						require.NoError(t, err)
						require.False(t, stat.Exists, "%s left its old path", path)
					}
					f.requireContents(t, "race", unrelated)
					f.requireContents(t, target, occupants)
				})
			}
		}
		t.Run(backend+"/same path is a no-op", func(t *testing.T) {
			f := newRaceFixture(t, backend, nil)
			f.writeFiles(t, "race", map[string]string{"/src/a.md": "a"})
			for _, overwrite := range []bool{false, true} {
				before := f.snapshotTenant(t)
				result, err := f.svc[0].Rename(f.ctx, f.auth, files.RenameRequest{Project: "race", FromPath: "/src", ToPath: "/src", Overwrite: overwrite})
				require.NoError(t, err)
				require.Zero(t, result.MovedCount)
				require.Equal(t, before, f.snapshotTenant(t))
			}
		})
	}
}

// TestRenameFileOverwriteContractUnchanged verifies the single-file contract
// beside the directory rule: a file replaces an existing destination file only
// with overwrite, and never replaces a directory.
func TestRenameFileOverwriteContractUnchanged(t *testing.T) {
	for _, backend := range renameTestBackends {
		for _, target := range renameTargets {
			t.Run(backend+"/"+target, func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				f.writeFiles(t, "race", map[string]string{"/src.md": "source"})
				f.writeFiles(t, target, map[string]string{"/dst.md": "replaced", "/dir/inner.md": "inner"})
				f.requireRejectedRename(t, files.RenameRequest{Project: "race", FromPath: "/src.md", TargetProject: target, ToPath: "/dst.md"}, files.ErrCodeAlreadyExists)
				f.requireRejectedRename(t, files.RenameRequest{Project: "race", FromPath: "/src.md", TargetProject: target, ToPath: "/dir", Overwrite: true}, files.ErrCodeAlreadyExists)
				f.requireRejectedRename(t, files.RenameRequest{Project: "race", FromPath: "/src.md", TargetProject: target, ToPath: "/dir/inner.md/x", Overwrite: true}, files.ErrCodeNotDirectory)

				result, err := f.svc[0].Rename(f.ctx, f.auth, files.RenameRequest{Project: "race", FromPath: "/src.md", TargetProject: target, ToPath: "/dst.md", Overwrite: true})
				require.NoError(t, err)
				require.Equal(t, 1, result.MovedCount)
				f.requireContents(t, target, map[string]string{"/dst.md": "source", "/dir/inner.md": "inner"})
			})
		}
	}
}

// fileID returns the row ID of the live file at path in project.
func (f *raceFixture) fileID(t *testing.T, project, path string) uint64 {
	t.Helper()
	var id uint64
	require.NoError(t, f.db[1].QueryRowContext(f.ctx, f.query(`SELECT id FROM mcp_files
		WHERE apikey_hash = ? AND project = ? AND path = ? AND deleted = FALSE AND system_owner = ?`),
		f.auth.APIKeyHash, project, path, "").Scan(&id))
	return id
}
