package files_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestDirectoryOperationsMatchALiteralPrefix verifies that delete, write,
// list and stat treat a directory's descendants as a literal, case-sensitive
// path prefix: "_" is a legal path character, not a wildcard, so siblings such
// as /aXb or /A_B are never part of /a_b.
func TestDirectoryOperationsMatchALiteralPrefix(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			c := newCredentialFixture(t, backend, func(s *files.Settings) { s.Search.Enabled = false })
			svc := c.service(t, 0, nil)
			write := func(path string) {
				t.Helper()
				_, err := svc.Write(c.ctx, c.auth, "race", path, "bytes of "+path, "utf-8", 0, files.WriteModeTruncate)
				require.NoError(t, err, path)
			}
			write("/a_b/x.md")
			directoryTime := c.clock.Now()
			c.clock.Advance(time.Hour)
			siblings := []string{"/aXb/keep.md", "/A_B/keep.md", "/a_bc/keep.md"}
			for _, path := range siblings {
				write(path)
			}

			listed, err := svc.List(c.ctx, c.auth, "race", "/a_b", 1, 100)
			require.NoError(t, err)
			paths := make([]string, 0, len(listed.Entries))
			for _, entry := range listed.Entries {
				paths = append(paths, entry.Path)
			}
			require.Equal(t, []string{"/a_b/x.md"}, paths, "list shows only the directory's own files")

			stat, err := svc.Stat(c.ctx, c.auth, "race", "/a_b")
			require.NoError(t, err)
			require.Equal(t, files.FileTypeDirectory, stat.Type)
			require.WithinDuration(t, directoryTime, stat.UpdatedAt, time.Second, "a sibling's update is not the directory's")

			for _, path := range []string{"/q_r", "/Q_R"} {
				stat, err = svc.Stat(c.ctx, c.auth, "race", path)
				require.NoError(t, err, path)
				require.False(t, stat.Exists, "%s has no files below it", path)
			}
			write("/qXr/file.md")
			stat, err = svc.Stat(c.ctx, c.auth, "race", "/q_r")
			require.NoError(t, err)
			require.False(t, stat.Exists, "/qXr is not /q_r")
			write("/q_r") // a file may take a path whose look-alike is a directory

			deleted, err := svc.Delete(c.ctx, c.auth, "race", "/a_b", true)
			require.NoError(t, err)
			require.Equal(t, 1, deleted.DeletedCount)
			for _, path := range siblings {
				read, err := svc.Read(c.ctx, c.auth, "race", path, 0, -1)
				require.NoError(t, err, "a recursive delete of /a_b must keep %s", path)
				require.Equal(t, "bytes of "+path, read.Content)
			}
		})
	}
}
