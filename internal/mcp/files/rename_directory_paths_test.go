package files_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// renameTestBackends runs a rename scenario on both storage backends; the
// PostgreSQL run is skipped without FILEIO_TEST_POSTGRES_DSN.
var renameTestBackends = []string{"sqlite", "postgres"}

// TestRenameDirectoryRejectsAFileOnAnInnerPath verifies a directory move cannot
// place files below an existing file of the destination, within a project or
// across projects, and leaves both sides unchanged.
func TestRenameDirectoryRejectsAFileOnAnInnerPath(t *testing.T) {
	for _, backend := range renameTestBackends {
		for _, target := range []string{"race", "race-peer"} {
			t.Run(backend+"/"+target, func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				svc := f.svc[0]
				_, err := svc.Write(f.ctx, f.auth, "race", "/src/sub/x.md", "child", "utf-8", 0, files.WriteModeTruncate)
				require.NoError(t, err)
				_, err = svc.Write(f.ctx, f.auth, target, "/out/sub", "a file", "utf-8", 0, files.WriteModeTruncate)
				require.NoError(t, err)

				_, err = svc.Rename(f.ctx, f.auth, files.RenameRequest{Project: "race", FromPath: "/src", TargetProject: target, ToPath: "/out"})
				require.True(t, files.IsCode(err, files.ErrCodeNotDirectory), "%v", err)
				read, err := svc.Read(f.ctx, f.auth, "race", "/src/sub/x.md", 0, -1)
				require.NoError(t, err)
				require.Equal(t, "child", read.Content)
				read, err = svc.Read(f.ctx, f.auth, target, "/out/sub", 0, -1)
				require.NoError(t, err)
				require.Equal(t, "a file", read.Content)
			})
		}
		t.Run(backend+"/a moved file frees its path", func(t *testing.T) {
			f := newRaceFixture(t, backend, nil)
			svc := f.svc[0]
			for path, content := range map[string]string{"/a/b/c": "file c", "/a/b/b/c/x.md": "deep"} {
				_, err := svc.Write(f.ctx, f.auth, "race", path, content, "utf-8", 0, files.WriteModeTruncate)
				require.NoError(t, err)
			}
			result, err := svc.Rename(f.ctx, f.auth, files.RenameRequest{Project: "race", FromPath: "/a/b", ToPath: "/a"})
			require.NoError(t, err, "/a/b/c moves to /a/c, so /a/b/c can become a directory")
			require.Equal(t, 2, result.MovedCount)
			for path, content := range map[string]string{"/a/c": "file c", "/a/b/c/x.md": "deep"} {
				read, err := svc.Read(f.ctx, f.auth, "race", path, 0, -1)
				require.NoError(t, err)
				require.Equal(t, content, read.Content)
			}
		})
	}
}

// TestRenameDirectoryIntoItsParentKeepsEachHistory verifies a move whose new
// path is another moved file's old path: /a/b/b/x.md takes /a/b/x.md while that
// file moves on to /a/x.md. Both files keep their bytes and their own history.
func TestRenameDirectoryIntoItsParentKeepsEachHistory(t *testing.T) {
	for _, backend := range renameTestBackends {
		t.Run(backend, func(t *testing.T) {
			f := newRaceFixture(t, backend, nil)
			svc := f.svc[0]
			for _, write := range []struct{ path, content string }{
				{"/a/b/b/x.md", "deep v1"}, {"/a/b/b/x.md", "deep v2"}, {"/a/b/b/x.md", "deep v3"},
				{"/a/b/x.md", "shallow v1"}, {"/a/b/x.md", "shallow v2"},
			} {
				_, err := svc.Write(f.ctx, f.auth, "race", write.path, write.content, "utf-8", 0, files.WriteModeTruncate)
				require.NoError(t, err)
			}
			deep, err := svc.ListVersions(f.ctx, f.auth, "race", "/a/b/b/x.md")
			require.NoError(t, err)
			shallow, err := svc.ListVersions(f.ctx, f.auth, "race", "/a/b/x.md")
			require.NoError(t, err)
			require.NotEqual(t, len(deep), len(shallow), "distinct history lengths make a mix-up visible")

			result, err := svc.Rename(f.ctx, f.auth, files.RenameRequest{Project: "race", FromPath: "/a/b", ToPath: "/a"})
			require.NoError(t, err)
			require.Equal(t, 2, result.MovedCount)
			for path, content := range map[string]string{"/a/b/x.md": "deep v3", "/a/x.md": "shallow v2"} {
				read, err := svc.Read(f.ctx, f.auth, "race", path, 0, -1)
				require.NoError(t, err)
				require.Equal(t, content, read.Content)
			}
			movedDeep, err := svc.ListVersions(f.ctx, f.auth, "race", "/a/b/x.md")
			require.NoError(t, err)
			movedShallow, err := svc.ListVersions(f.ctx, f.auth, "race", "/a/x.md")
			require.NoError(t, err)
			require.Len(t, movedDeep, len(deep), "the deep file keeps its own history")
			require.Len(t, movedShallow, len(shallow), "the shallow file keeps its own history")
		})
	}
}
