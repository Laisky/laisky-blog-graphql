package benchmark

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	pageindexplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/pageindex"
)

func TestPageIndexPersistsAndSearchesThroughRealSystemFS(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dsn := fmt.Sprintf("file:pageindex-systemfs-regression-%d?mode=memory&cache=shared&_foreign_keys=on", time.Now().UnixNano())
	db, err := sql.Open("sqlite3", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() { require.NoError(t, db.Close()) }()

	settings := localFileSettings()
	protector, err := files.NewCredentialProtector(settings.Security)
	require.NoError(t, err)
	service, err := files.NewService(db, settings, nil, nil, protector, newLocalCredentialStore(), nil, nil, nil)
	require.NoError(t, err)
	plugin, err := newLocalPageIndex(service)
	require.NoError(t, err)
	defer func() { require.NoError(t, plugin.Stop(context.Background())) }()

	auth := files.AuthContext{
		APIKey: "pageindex-systemfs-regression",
		APIKeyHash: "pageindex-systemfs-regression",
		UserIdentity: "user:pageindex-systemfs-regression",
	}
	const project = "pageindex-systemfs-regression"
	const documentPath = "/manual.md"
	content := "# Manual\n\n## Rollback\n\nThe emergency rollback token is ORBIT-17."
	_, err = plugin.Write(ctx, auth, project, documentPath, content, "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)

	systemFS, err := service.SystemNamespace(pageindexplugin.SysOwner)
	require.NoError(t, err)
	store := pageindexplugin.NewSysStore(systemFS)
	index, err := store.GetIndex(ctx, project)
	require.NoError(t, err)
	require.Contains(t, index, documentPath, "PageIndex must persist the user-path catalog through the production SystemFS implementation")
	require.NotEmpty(t, index[documentPath].DocID)
	tree, err := store.GetTree(ctx, project, index[documentPath].DocID)
	require.NoError(t, err)
	require.NotEmpty(t, tree.Structure)

	result, err := plugin.Search(ctx, auth, project, "Which token is required for rollback?", "/", 5)
	require.NoError(t, err)
	require.NotEmpty(t, result.Chunks)
	var retrieved strings.Builder
	for _, chunk := range result.Chunks {
		require.Equal(t, documentPath, chunk.FilePath)
		retrieved.WriteString(chunk.ChunkContent)
		retrieved.WriteByte('\n')
	}
	require.Contains(t, retrieved.String(), "ORBIT-17", "PageIndex may return the document heading before the matching child node, so evidence is asserted across the ranked result set")
}
