package files_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
	ragplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/rag"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
)

// TestDefaultRenameDirectoryRequiresVacantDestination verifies the directory
// relocation rule with the @DEFAULT layer: a same-project rename is judged on
// the merged view, so a visible default occupies the destination, and every
// physical destination must be vacant too; a cross-project move checks only
// the physical target project. Each rejection, with either overwrite value,
// changes no file, history or outbox row in any project.
func TestDefaultRenameDirectoryRequiresVacantDestination(t *testing.T) {
	cases := []struct {
		name   string
		layers map[string]map[string]string
		target string
		toPath string
		code   files.ErrorCode
	}{
		{"visible default below the destination", map[string]map[string]string{
			"race": {"/src/a.md": "a"}, files.DefaultProject: {"/dst/x.md": "default"},
		}, "race", "/dst", files.ErrCodeAlreadyExists},
		{"visible default at the destination", map[string]map[string]string{
			"race": {"/src/a.md": "a"}, files.DefaultProject: {"/dst": "default"},
		}, "race", "/dst", files.ErrCodeAlreadyExists},
		{"visible default file ancestor", map[string]map[string]string{
			"race": {"/src/a.md": "a"}, files.DefaultProject: {"/d": "default"},
		}, "race", "/d/dst", files.ErrCodeNotDirectory},
		{"mixed-layer source into an occupied project directory", map[string]map[string]string{
			"race": {"/src/a.md": "a", "/dst/other.md": "other"}, files.DefaultProject: {"/src/b.md": "b"},
		}, "race", "/dst", files.ErrCodeAlreadyExists},
		{"mixed-layer source into an occupied physical target", map[string]map[string]string{
			"race": {"/src/a.md": "a"}, files.DefaultProject: {"/src/b.md": "b"}, "race-peer": {"/dst/other.md": "other"},
		}, "race-peer", "/dst", files.ErrCodeAlreadyExists},
		{"mixed-layer source below a physical target file", map[string]map[string]string{
			"race": {"/src/a/b.md": "b"}, files.DefaultProject: {"/src/c.md": "c"}, "race-peer": {"/dst/a": "a file"},
		}, "race-peer", "/dst", files.ErrCodeAlreadyExists},
	}
	for _, backend := range renameTestBackends {
		for _, tc := range cases {
			t.Run(backend+"/"+tc.name, func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				for project, contents := range tc.layers {
					f.writeFiles(t, project, contents)
				}
				req := files.RenameRequest{Project: "race", FromPath: "/src", TargetProject: tc.target, ToPath: tc.toPath}
				f.requireRejectedRename(t, req, tc.code)
				req.Overwrite = true
				f.requireRejectedRename(t, req, files.ErrCodeInvalidArgument)
				for project, contents := range tc.layers {
					f.requireContents(t, project, contents)
				}
			})
		}
	}
}

// TestDefaultRenameMixedLayerDirectoryToVacantPath verifies a directory whose
// visible files live in both layers relocates to a vacant path. Within the
// project every file is renamed in the physical project that stores it, so the
// inherited part moves inside @DEFAULT. Across projects every file lands in
// the target project; the second physical group's destination check must not
// mistake the first group's just-moved files for an occupied destination. A
// target that only inherits a default at the destination is vacant, because
// the cross-project check is physical.
func TestDefaultRenameMixedLayerDirectoryToVacantPath(t *testing.T) {
	for _, backend := range renameTestBackends {
		t.Run(backend+"/same project", func(t *testing.T) {
			f := newRaceFixture(t, backend, nil)
			f.writeFiles(t, "race", map[string]string{"/src/a.md": "own"})
			f.writeFiles(t, files.DefaultProject, map[string]string{"/src/b.md": "inherited"})
			ownID, inheritedID := f.fileID(t, "race", "/src/a.md"), f.fileID(t, files.DefaultProject, "/src/b.md")

			result, err := f.svc[0].Rename(f.ctx, f.auth, files.RenameRequest{Project: "race", FromPath: "/src", ToPath: "/dst"})
			require.NoError(t, err)
			require.Equal(t, 2, result.MovedCount)
			require.Equal(t, ownID, f.fileID(t, "race", "/dst/a.md"))
			require.Equal(t, inheritedID, f.fileID(t, files.DefaultProject, "/dst/b.md"), "the inherited file is renamed inside @DEFAULT")
			f.requireContents(t, "race", map[string]string{"/dst/a.md": "own", "/dst/b.md": "inherited"})
		})
		for _, inherited := range []bool{false, true} {
			name := backend + "/cross project"
			if inherited {
				name += " onto an inherited-only destination"
			}
			t.Run(name, func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				f.writeFiles(t, "race", map[string]string{"/src/a.md": "own", "/src/sub/c.md": "deep"})
				f.writeFiles(t, files.DefaultProject, map[string]string{"/src/b.md": "inherited"})
				if inherited {
					f.writeFiles(t, files.DefaultProject, map[string]string{"/dst/x.md": "shown in every project"})
				}
				inheritedID := f.fileID(t, files.DefaultProject, "/src/b.md")

				result, err := f.svc[0].Rename(f.ctx, f.auth, files.RenameRequest{Project: "race", FromPath: "/src", TargetProject: "race-peer", ToPath: "/dst"})
				require.NoError(t, err)
				require.Equal(t, 3, result.MovedCount)
				require.Equal(t, inheritedID, f.fileID(t, "race-peer", "/dst/b.md"), "the inherited file moves out of @DEFAULT")
				f.requireContents(t, "race-peer", map[string]string{"/dst/a.md": "own", "/dst/sub/c.md": "deep", "/dst/b.md": "inherited"})
				for _, path := range []string{"/src/a.md", "/src/b.md", "/src/sub/c.md"} {
					stat, err := f.svc[0].Stat(f.ctx, f.auth, "race", path)
					require.NoError(t, err)
					require.False(t, stat.Exists, "%s left the source view", path)
				}
				if inherited {
					f.requireContents(t, "race-peer", map[string]string{"/dst/x.md": "shown in every project"})
					f.requireContents(t, files.DefaultProject, map[string]string{"/dst/x.md": "shown in every project"})
				}
			})
		}
	}
}

// TestDefaultAppendToInheritedFileRunsOnce verifies an APPEND through the MCP
// file tool and the plugin manager to a file a project inherits from @DEFAULT
// changes that @DEFAULT file exactly once: its content gains the appended
// bytes once, @DEFAULT gains exactly one history row and one RAG upsert, the
// project gains no file, and nothing changes after the manager stops.
func TestDefaultAppendToInheritedFileRunsOnce(t *testing.T) {
	for _, backend := range renameTestBackends {
		t.Run(backend, func(t *testing.T) {
			f := newRaceFixture(t, backend, func(s *files.Settings) { s.Index.Workers = 0 })
			f.writeFiles(t, files.DefaultProject, map[string]string{"/shared.md": "A"})
			stat, err := f.svc[0].Stat(f.ctx, f.auth, "race", "/shared.md")
			require.NoError(t, err)
			require.True(t, stat.Exists, "race inherits the default")

			rag, err := ragplugin.New(f.svc[0])
			require.NoError(t, err)
			manager, err := mcpplugin.NewManager(mcpplugin.DefaultPluginRAG, rag)
			require.NoError(t, err)
			require.NoError(t, manager.StartAll(f.ctx))
			writer, err := tools.NewFileWriteTool(manager)
			require.NoError(t, err)

			before := f.snapshotTenant(t)
			callRaceTool(t, f.toolContext(), writer, "file_write", map[string]any{
				"project": "race", "path": "shared.md", "content": "B", "mode": "APPEND", "expected_version": stat.Version,
			})
			require.NoError(t, manager.StopAll(f.ctx))
			after := f.snapshotTenant(t)

			f.requireContents(t, files.DefaultProject, map[string]string{"/shared.md": "AB"})
			require.Len(t, after.files, len(before.files), "no project copy is created")
			newVersions, newJobs := after.versions[len(before.versions):], after.jobs[len(before.jobs):]
			require.Len(t, newVersions, 1, "exactly one history row")
			require.Contains(t, newVersions[0], files.DefaultProject+"/shared.md", "the history row belongs to @DEFAULT")
			require.Len(t, newJobs, 1, "exactly one outbox job")
			require.Contains(t, newJobs[0], files.DefaultProject+"/shared.md", "the job indexes the @DEFAULT file")
			require.True(t, strings.HasSuffix(strings.ToUpper(newJobs[0]), "UPSERT"), newJobs[0])
			require.Equal(t, after, f.snapshotTenant(t), "nothing changes after the call returns")
		})
	}
}
