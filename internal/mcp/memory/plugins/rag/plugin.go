package rag

import (
	"context"
	"time"

	errors "github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// Plugin wraps the existing files.Service behind the plugin contract.
type Plugin struct {
	inner *files.Service
}

// New constructs the rag plugin around the existing file service.
func New(inner *files.Service) (*Plugin, error) {
	if inner == nil {
		return nil, errors.New("file service is required")
	}

	return &Plugin{inner: inner}, nil
}

// Name returns the stable plugin identifier.
func (p *Plugin) Name() string {
	return mcpplugin.DefaultPluginRAG
}

// Capabilities returns the current rag plugin behavior contract.
func (p *Plugin) Capabilities() mcpplugin.Capabilities {
	return mcpplugin.Capabilities{
		SearchModes:      []mcpplugin.SearchMode{mcpplugin.SearchModeHybrid, mcpplugin.SearchModeSemantic, mcpplugin.SearchModeLexical},
		SupportsRandomIO: true,
		SupportsVersions: true,
		AsyncIndexing:    true,
		FreshnessWindow:  5 * time.Second,
		Notes:            "wraps the existing Postgres + pgvector + BM25 file service",
	}
}

// Start starts background indexing workers for the wrapped file service.
func (p *Plugin) Start(ctx context.Context) error {
	return p.inner.StartIndexWorkers(ctx)
}

// Stop stops the rag plugin.
func (p *Plugin) Stop(context.Context) error {
	return nil
}

// Stat delegates file_stat to the wrapped file service.
func (p *Plugin) Stat(ctx context.Context, auth files.AuthContext, project, path string) (files.StatResult, error) {
	return p.inner.Stat(ctx, auth, project, path)
}

// Read delegates file_read to the wrapped file service.
func (p *Plugin) Read(ctx context.Context, auth files.AuthContext, project, path string, offset, length int64) (files.ReadResult, error) {
	return p.inner.Read(ctx, auth, project, path, offset, length)
}

// Write delegates file_write to the wrapped file service.
func (p *Plugin) Write(ctx context.Context, auth files.AuthContext, project, path, content, contentEncoding string, offset int64, mode files.WriteMode) (files.WriteResult, error) {
	return p.inner.Write(ctx, auth, project, path, content, contentEncoding, offset, mode)
}

// Delete delegates file_delete to the wrapped file service.
func (p *Plugin) Delete(ctx context.Context, auth files.AuthContext, project, path string, recursive bool) (files.DeleteResult, error) {
	return p.inner.Delete(ctx, auth, project, path, recursive)
}

// Rename delegates file_rename to the wrapped file service.
func (p *Plugin) Rename(ctx context.Context, auth files.AuthContext, project, fromPath, toPath string, overwrite bool) (files.RenameResult, error) {
	return p.inner.Rename(ctx, auth, project, fromPath, toPath, overwrite)
}

// List delegates file_list to the wrapped file service.
func (p *Plugin) List(ctx context.Context, auth files.AuthContext, project, path string, depth, limit int) (files.ListResult, error) {
	return p.inner.List(ctx, auth, project, path, depth, limit)
}

// Search delegates file_search to the wrapped file service.
func (p *Plugin) Search(ctx context.Context, auth files.AuthContext, project, query, pathPrefix string, limit int) (files.SearchResult, error) {
	return p.inner.Search(ctx, auth, project, query, pathPrefix, limit)
}
