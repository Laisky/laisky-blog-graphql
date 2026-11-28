package fileio

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// restoreWritePlugin records the conditions of every Write it receives.
type restoreWritePlugin struct {
	mcpplugin.Plugin
	writes     int
	conditions files.FilePreconditions
}

func (*restoreWritePlugin) SupportsFileVersionPreconditions() bool { return true }

func (p *restoreWritePlugin) Write(ctx context.Context, auth files.AuthContext, project, path, _, _ string, _ int64, _ files.WriteMode) (files.WriteResult, error) {
	p.writes++
	p.conditions, _ = files.ScopedFilePreconditions(ctx, auth, project, path, files.FileOperationWrite)
	return files.WriteResult{BytesWritten: 3, Version: "00000000000000000000000000000002:2"}, nil
}

// restoreHistoryProbe records the restore conditions scoped to a history read
// and returns refusal when it is set.
type restoreHistoryProbe struct {
	files.HistoryReader
	scoped     bool
	conditions files.FilePreconditions
	refusal    error
}

func (p *restoreHistoryProbe) ReadVersion(ctx context.Context, auth files.AuthContext, project, path string, id uint64) (files.FileVersion, error) {
	p.conditions, p.scoped = files.ScopedFilePreconditions(ctx, auth, project, path, files.FileOperationRestore)
	if p.refusal != nil {
		return files.FileVersion{}, p.refusal
	}
	return files.FileVersion{ID: id, Project: project, Path: path, Content: []byte("old")}, nil
}

// TestGraphQLRestoreChecksConditionsOnTheHistoryRead requires FileRestoreVersion
// to hand its current-file conditions to the history read, as MCP does, so the
// service can refuse a snapshot whose namespace is not the conditioned file's,
// and to stop before the write when the read refuses.
func TestGraphQLRestoreChecksConditionsOnTheHistoryRead(t *testing.T) {
	ctx := authtest.WithVerified(context.Background(), "synthetic-key")
	const version = "00000000000000000000000000000001:1"
	backend, history := &restoreWritePlugin{}, &restoreHistoryProbe{}
	resolver := &MutationResolver{NewResolver(backend, history, nil, nil)}

	expected := version
	_, err := resolver.FileRestoreVersion(ctx, "alpha", "a.md", "7", &expected, nil, nil)
	require.NoError(t, err)
	require.True(t, history.scoped, "the history read is scoped to the restore")
	require.Equal(t, files.FilePreconditions{ExpectedVersion: version}, history.conditions)
	require.Equal(t, 1, backend.writes)
	require.Equal(t, version, backend.conditions.ExpectedVersion, "the write is still conditioned")

	createOnly := true
	_, err = resolver.FileRestoreVersion(ctx, "alpha", "a.md", "7", nil, &createOnly, nil)
	require.NoError(t, err)
	require.Equal(t, files.FilePreconditions{CreateOnly: true}, history.conditions)

	history.refusal = files.NewError(files.ErrCodeVersionConflict, "file version changed", false)
	_, err = resolver.FileRestoreVersion(ctx, "alpha", "a.md", "7", &expected, nil, nil)
	var gqlErr *gqlerror.Error
	require.ErrorAs(t, err, &gqlErr)
	require.Equal(t, string(files.ErrCodeVersionConflict), gqlErr.Extensions["code"])
	require.Equal(t, 2, backend.writes, "a refused history read never reaches the write")
}
