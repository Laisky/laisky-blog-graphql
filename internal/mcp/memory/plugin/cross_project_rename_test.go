package plugin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// movingTestPlugin adds the cross-project capability and records its calls.
type movingTestPlugin struct {
	testPlugin
	renames, moves int
	target         string
}

// Rename records a same-project rename.
func (p *movingTestPlugin) Rename(context.Context, files.AuthContext, string, string, string, bool) (files.RenameResult, error) {
	p.renames++
	return files.RenameResult{MovedCount: 1}, nil
}

// RenameAcrossProjects records a cross-project move.
func (p *movingTestPlugin) RenameAcrossProjects(_ context.Context, _ files.AuthContext, _, _, targetProject, _ string, _ bool) (files.RenameResult, error) {
	p.moves++
	p.target = targetProject
	return files.RenameResult{MovedCount: 1}, nil
}

// TestManagerRenameAcrossProjectsRouting verifies that the manager forwards a
// cross-project move only to a capable plugin and never degrades it into a
// same-project rename.
func TestManagerRenameAcrossProjectsRouting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	capable := &movingTestPlugin{testPlugin: testPlugin{name: DefaultPluginRAG}}
	mgr, err := NewManager(DefaultPluginRAG, capable)
	require.NoError(t, err)
	var _ CrossProjectRenamer = mgr

	_, err = mgr.RenameAcrossProjects(ctx, files.AuthContext{}, "alpha", "/a", "beta", "/b", false)
	require.NoError(t, err)
	require.Equal(t, 1, capable.moves)
	require.Equal(t, "beta", capable.target)

	for _, target := range []string{"", "alpha"} {
		_, err = mgr.RenameAcrossProjects(ctx, files.AuthContext{}, "alpha", "/a", target, "/b", false)
		require.NoError(t, err)
	}
	require.Equal(t, 2, capable.renames, "an empty or identical target is an ordinary rename")
	require.Equal(t, 1, capable.moves)

	incapable := &testPlugin{name: DefaultPluginRAG}
	mgr, err = NewManager(DefaultPluginRAG, incapable)
	require.NoError(t, err)
	_, err = mgr.RenameAcrossProjects(ctx, files.AuthContext{}, "alpha", "/a", "beta", "/b", false)
	require.True(t, files.IsCode(err, files.ErrCodeInvalidArgument), "%v", err)
	require.ErrorContains(t, err, "target_project")
}
