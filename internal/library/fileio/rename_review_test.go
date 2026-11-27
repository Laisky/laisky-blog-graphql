package fileio

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

type renameReviewPlugin struct {
	mcpplugin.Plugin
	calls      int
	target     string
	conditions files.FilePreconditions
}

func (*renameReviewPlugin) SupportsFileVersionPreconditions() bool { return true }

func (p *renameReviewPlugin) RenameAcrossProjects(ctx context.Context, auth files.AuthContext, project, from, target, _ string, _ bool) (files.RenameResult, error) {
	p.calls++
	p.target = target
	p.conditions, _ = files.ScopedFilePreconditions(ctx, auth, project, from, files.FileOperationRename)
	return files.RenameResult{MovedCount: 1}, nil
}

func TestGraphQLRenameTargetValidatedBeforeDispatch(t *testing.T) {
	ctx := authtest.WithVerified(context.Background(), "synthetic-key")
	const version = "00000000000000000000000000000001:1"
	backend := &renameReviewPlugin{}
	resolver := &MutationResolver{NewResolver(backend, nil, nil, nil)}
	for _, invalid := range []string{"*", "bad/project", "bad project", strings.Repeat("a", 129)} {
		_, err := resolver.FileRename(ctx, "source", "old.txt", "folder/new.txt", &invalid, false, version, nil, nil, nil)
		require.Error(t, err)
		require.Zero(t, backend.calls)
	}
	target := "archive"
	result, err := resolver.FileRename(ctx, "source", "old.txt", "folder/new.txt", &target, false, version, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, backend.calls)
	require.Equal(t, "archive", backend.target)
	require.Equal(t, "archive", backend.conditions.DestinationProject)
	require.Equal(t, "/folder/new.txt", backend.conditions.DestinationPath)
}
