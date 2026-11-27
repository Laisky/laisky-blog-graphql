package files_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestRenameOverwriteKeepsTheReplacedContentInPathHistory verifies an
// overwriting rename replaces the destination the way a write does: history
// belongs to the destination path, and the replaced bytes stay restorable
// there, within a project and across projects.
func TestRenameOverwriteKeepsTheReplacedContentInPathHistory(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		for _, target := range []string{"race", "race-peer"} {
			t.Run(backend+"/"+target, func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				svc := f.svc[0]
				_, err := svc.Write(f.ctx, f.auth, target, "/b.txt", "replaced bytes", "utf-8", 0, files.WriteModeTruncate)
				require.NoError(t, err)
				_, err = svc.Write(f.ctx, f.auth, "race", "/a.txt", "moved bytes", "utf-8", 0, files.WriteModeTruncate)
				require.NoError(t, err)

				_, err = svc.Rename(f.ctx, f.auth, files.RenameRequest{Project: "race", FromPath: "/a.txt", TargetProject: target, ToPath: "/b.txt", Overwrite: true})
				require.NoError(t, err)
				read, err := svc.Read(f.ctx, f.auth, target, "/b.txt", 0, -1)
				require.NoError(t, err)
				require.Equal(t, "moved bytes", read.Content)

				versions, err := svc.ListVersions(f.ctx, f.auth, target, "/b.txt")
				require.NoError(t, err)
				var contents []string
				var replaced uint64
				for _, version := range versions {
					full, err := svc.ReadVersion(f.ctx, f.auth, target, "/b.txt", version.ID)
					require.NoError(t, err)
					contents = append(contents, string(full.Content))
					if string(full.Content) == "replaced bytes" {
						replaced = version.ID
					}
				}
				require.NotZero(t, replaced, "the replaced content is in the path's history: %q", contents)

				_, err = svc.RestoreVersion(f.ctx, f.auth, target, "/b.txt", replaced)
				require.NoError(t, err)
				read, err = svc.Read(f.ctx, f.auth, target, "/b.txt", 0, -1)
				require.NoError(t, err)
				require.Equal(t, "replaced bytes", read.Content, "restoring brings the replaced bytes back")
			})
		}
	}
}
