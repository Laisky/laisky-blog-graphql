package pageindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// PluginDeps gathers the runtime collaborators the plugin needs.
type PluginDeps struct {
	UserFS    *files.Service
	SystemFS  files.SystemFS
	Settings  Settings
	LLM       LLM
	Tokenizer Tokenizer
	PDF       PDFParser
	Cache     Cache
	Logger    logSDK.Logger
}

// Plugin satisfies mcpplugin.Plugin for long-document tree-reasoning memory.
type Plugin struct {
	userFS   *files.Service
	sysFS    files.SystemFS
	indexer  *Indexer
	store    *SysStore
	searcher *Searcher
	cfg      Settings
	log      logSDK.Logger
	cache    Cache
}

// New constructs the plugin from deps. It does not open the bbolt cache or
// touch the LLM client; that work happens in Start so a missing api_key never
// breaks process startup (per §2.7).
func New(deps PluginDeps) (*Plugin, error) {
	if deps.UserFS == nil {
		return nil, errors.New("userFS is nil")
	}
	if deps.SystemFS == nil {
		return nil, errors.New("systemFS is nil")
	}
	return &Plugin{
		userFS: deps.UserFS,
		sysFS:  deps.SystemFS,
		store:  NewSysStore(deps.SystemFS),
		cfg:    deps.Settings,
		log:    deps.Logger,
		// Indexer / searcher are lazily wired in Start so test injection works.
		indexer:  buildIndexerForCtor(deps),
		searcher: nil,
		cache:    deps.Cache,
	}, nil
}

func buildIndexerForCtor(deps PluginDeps) *Indexer {
	if deps.LLM == nil || deps.Tokenizer == nil {
		return nil
	}
	idx, err := NewIndexer(Deps{
		LLM:       deps.LLM,
		PDF:       deps.PDF,
		Tokenizer: deps.Tokenizer,
		Cache:     deps.Cache,
		Settings:  deps.Settings,
		Logger:    deps.Logger,
	})
	if err != nil {
		return nil
	}
	return idx
}

// Name returns "pageindex".
func (p *Plugin) Name() string { return "pageindex" }

// Capabilities advertises the §2.2 profile for the long-doc engine.
func (p *Plugin) Capabilities() mcpplugin.Capabilities {
	return mcpplugin.Capabilities{
		SearchModes:      []mcpplugin.SearchMode{mcpplugin.SearchModeTreeReasoning},
		SupportsRandomIO: true,
		SupportsRename:   true,
		SupportsVersions: false,
		AsyncIndexing:    false,
		FreshnessWindow:  0,
		MaxPayloadBytes:  0,
		Notes:            "vectorless tree-reasoning over long docs",
	}
}

// Start opens the bbolt cache and wires the searcher.
func (p *Plugin) Start(ctx context.Context) error {
	if !p.cfg.Enabled() {
		return errors.New("pageindex disabled (llm.api_key empty)")
	}
	if p.indexer == nil {
		return errors.New("pageindex indexer was not constructed (missing llm/tokenizer in PluginDeps)")
	}
	if p.cache == nil {
		c, err := NewCache(CacheConfig{
			Enabled:      p.cfg.Indexer.Cache.Enabled,
			Path:         p.cfg.Indexer.Cache.Path,
			MaxSizeBytes: p.cfg.Indexer.Cache.MaxSizeBytes,
		})
		if err != nil {
			return errors.Wrap(err, "open cache")
		}
		p.cache = c
		p.indexer.cache = c
	}
	if p.searcher == nil {
		p.searcher = NewSearcher(p.indexer.llm, p.store, p.indexer, p.cfg)
	}
	return nil
}

// Stop closes the bbolt cache.
func (p *Plugin) Stop(ctx context.Context) error {
	if p.cache != nil {
		return p.cache.Close()
	}
	return nil
}

// Stat forwards to userFS unchanged.
func (p *Plugin) Stat(ctx context.Context, auth files.AuthContext, project, path string) (files.StatResult, error) {
	return p.userFS.Stat(ctx, auth, project, path)
}

// Read forwards to userFS unchanged.
func (p *Plugin) Read(ctx context.Context, auth files.AuthContext, project, path string, offset, length int64) (files.ReadResult, error) {
	return p.userFS.Read(ctx, auth, project, path, offset, length)
}

// Write persists the user document and synchronously builds and stores its PageIndex
// tree. Indexing and summarization always run against the complete post-write file
// re-read from storage, never merely the write argument, so APPEND/OVERWRITE/TRUNCATE
// describe the whole content (§4.5). Every stored file — including unsupported
// extensions — receives a bounded user-row summary published through the conditional
// publisher. A long-document write returns an error unless its tree and catalog are
// durable, preserving the plugin's advertised zero-second freshness window.
func (p *Plugin) Write(ctx context.Context, auth files.AuthContext, project, path, content, encoding string, offset int64, mode files.WriteMode) (files.WriteResult, error) {
	if isLongDocPath(path) && mode == files.WriteModeOverwrite && offset > 0 {
		return files.WriteResult{}, errors.New("INVALID_ARGUMENT: pageindex rejects OVERWRITE@offset on .pdf/.md paths; use file_delete then file_write instead")
	}
	res, err := p.userFS.WriteWith(ctx, auth, project, path, content, encoding, offset, mode, files.WriteOpts{SkipRAGIndex: true})
	if err != nil {
		return res, err
	}

	// Re-read the complete post-write content so APPEND/OVERWRITE/TRUNCATE index and
	// summarize the whole file, not just the request delta.
	full, readErr := p.userFS.Read(ctx, auth, project, path, 0, -1)
	if readErr != nil {
		return res, errors.Wrap(readErr, "read post-write content for pageindex")
	}
	fullBytes := []byte(full.Content)
	sourceHash := files.HashFileContent(fullBytes)
	storageProject := PageIndexStorageProject(auth, project)

	if !isLongDocPath(path) {
		// Unsupported extension (or no indexer): store a local deterministic summary
		// without routing through RAG or exposing a false search hit.
		desc := files.DeterministicFileSummaryFallback(full.Content, docSummaryMaxWords, docSummaryMaxBytes)
		if _, err := p.publishUserRowSummary(ctx, auth, project, path, desc, files.SummarySourceDeterministicFallback, sourceHash); err != nil {
			return res, err
		}
		return res, nil
	}
	if p.indexer == nil {
		return res, errors.New("pageindex indexer is unavailable")
	}

	kind := KindPDF
	if strings.HasSuffix(strings.ToLower(path), ".md") {
		kind = KindMarkdown
	}
	docID := docIDFromAuth(auth, project, path)
	tree, _, indexErr := p.indexer.Index(ctx, kind, fullBytes, IndexOptions{DocID: docID}, nil)
	if indexErr != nil {
		// Indexing errors should not silently fail the write; surface as warning and
		// still publish a deterministic user-row summary for the content generation.
		if p.log != nil {
			p.log.Warn("pageindex.write index error: " + indexErr.Error())
		}
		desc := files.DeterministicFileSummaryFallback(full.Content, docSummaryMaxWords, docSummaryMaxBytes)
		if _, err := p.publishUserRowSummary(ctx, auth, project, path, desc, files.SummarySourceDeterministicFallback, sourceHash); err != nil {
			return res, err
		}
		return res, nil
	}

	tree.SourceContentHash = sourceHash
	desc, source := finalizeDocDescription(tree, fullBytes)
	tree.DocDescription = desc

	if atomicFS, ok := p.sysFS.(files.AtomicSystemFS); ok {
		entry := IndexEntry{
			DocID:             docID,
			Type:              string(kind),
			PageCount:         tree.PageCount,
			LineCount:         tree.LineCount,
			IndexedAt:         time.Now().UTC().Format(time.RFC3339Nano),
			SourceContentHash: sourceHash,
		}
		published, err := atomicFS.PublishPluginSummaryAndState(
			ctx,
			auth,
			project,
			storageProject,
			path,
			pageIndexSummaryInput(desc, source, sourceHash),
			pageIndexWriteState(docID, path, tree, entry),
		)
		if err != nil {
			return res, err
		}
		if !published && p.log != nil {
			p.log.Debug("pageindex.write skipped stale tree publish: " + path)
		}
		return res, nil
	}

	// Publish the user-row summary first, guarded by the content hash. A false result
	// means a newer generation is already active, so this stale writer must not
	// overwrite the tree or index mapping (P06).
	published, err := p.publishUserRowSummary(ctx, auth, project, path, desc, source, sourceHash)
	if err != nil {
		return res, err
	}
	if !published {
		if p.log != nil {
			p.log.Debug("pageindex.write skipped stale tree publish: " + path)
		}
		return res, nil
	}

	if err := p.store.PutTree(ctx, storageProject, docID, tree); err != nil {
		return res, errors.Wrap(err, "persist pageindex tree")
	}
	entry := IndexEntry{
		DocID:             docID,
		Type:              string(kind),
		PageCount:         tree.PageCount,
		LineCount:         tree.LineCount,
		IndexedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		SourceContentHash: sourceHash,
	}
	if err := p.store.UpdateIndexEntry(ctx, storageProject, path, entry); err != nil {
		// Keep the deterministic tree. Deleting it here can corrupt an existing
		// catalog entry because updates reuse the same docID. A retry safely
		// overwrites the tree and converges the catalog and metadata.
		return res, errors.Wrap(err, "persist pageindex catalog")
	}
	return res, nil
}

// publishUserRowSummary publishes bounded summary catalog metadata onto the user file
// row through the conditional publisher. It returns whether the summary was published
// (false when a newer content generation is already active).
func (p *Plugin) publishUserRowSummary(ctx context.Context, auth files.AuthContext, project, path, summary string, source files.SummarySource, contentHash string) (bool, error) {
	published, err := p.userFS.PublishPluginSummary(ctx, auth, project, path, pageIndexSummaryInput(summary, source, contentHash))
	if err != nil {
		return false, errors.Wrap(err, "persist pageindex summary")
	}
	return published, nil
}

// Delete removes matching PageIndex trees and catalog entries before deleting
// user files, so failures remain fail-closed and the same request can be retried.
func (p *Plugin) Delete(ctx context.Context, auth files.AuthContext, project, path string, recursive bool) (files.DeleteResult, error) {
	if atomicFS, ok := p.sysFS.(files.AtomicSystemFS); ok {
		return atomicFS.DeleteWithState(ctx, auth, project, PageIndexStorageProject(auth, project), path, recursive, pageIndexDeleteState(path, recursive))
	}
	storageProject := PageIndexStorageProject(auth, project)
	entries, err := p.indexEntriesForPath(ctx, storageProject, path, recursive)
	if err != nil {
		return files.DeleteResult{}, errors.Wrap(err, "list pageindex catalog entries for delete")
	}
	for _, indexed := range entries {
		if err := p.store.DeleteTree(ctx, storageProject, indexed.entry.DocID); err != nil {
			return files.DeleteResult{}, errors.Wrapf(err, "delete pageindex tree for %s", indexed.path)
		}
		if _, _, err := p.store.RemoveIndexEntry(ctx, storageProject, indexed.path); err != nil {
			return files.DeleteResult{}, errors.Wrapf(err, "remove pageindex catalog entry %s", indexed.path)
		}
	}

	res, err := p.userFS.Delete(ctx, auth, project, path, recursive)
	if err != nil {
		if files.IsCode(err, files.ErrCodeNotFound) && len(entries) > 0 {
			return files.DeleteResult{DeletedCount: len(entries)}, nil
		}
		return res, err
	}
	return res, nil
}

// Rename persists the PageIndex path mapping before moving the user file. If
// the user-file rename fails, the catalog move is rolled back when possible.
func (p *Plugin) Rename(ctx context.Context, auth files.AuthContext, project, src, dst string, overwrite bool) (files.RenameResult, error) {
	if atomicFS, ok := p.sysFS.(files.AtomicSystemFS); ok {
		return atomicFS.RenameWithState(ctx, auth, project, PageIndexStorageProject(auth, project), src, dst, overwrite, pageIndexRenameState(src, dst, overwrite))
	}
	storageProject := PageIndexStorageProject(auth, project)
	index, err := p.store.GetIndex(ctx, storageProject)
	if err != nil {
		return files.RenameResult{}, errors.Wrap(err, "read pageindex catalog for rename")
	}
	_, indexed := index[src]
	if indexed {
		if err := p.store.RenameIndexEntry(ctx, storageProject, src, dst); err != nil {
			return files.RenameResult{}, errors.Wrap(err, "rename pageindex catalog entry")
		}
	}

	res, err := p.userFS.Rename(ctx, auth, project, src, dst, overwrite)
	if err != nil {
		if indexed {
			if rollbackErr := p.store.RenameIndexEntry(ctx, storageProject, dst, src); rollbackErr != nil {
				return res, errors.Wrapf(err, "rename user file; rollback pageindex catalog failed: %v", rollbackErr)
			}
		}
		return res, err
	}
	return res, nil
}

// List forwards to userFS unchanged.
func (p *Plugin) List(ctx context.Context, auth files.AuthContext, project, path string, depth, limit int) (files.ListResult, error) {
	return p.userFS.List(ctx, auth, project, path, depth, limit)
}

// Search runs the §2.6.2 tree-reasoning loop.
func (p *Plugin) Search(ctx context.Context, auth files.AuthContext, project, query, pathPrefix string, limit int) (files.SearchResult, error) {
	if p.searcher == nil {
		return files.SearchResult{}, errors.New("pageindex search not started")
	}
	if strings.TrimSpace(auth.APIKeyHash) == "" {
		return files.SearchResult{}, errors.New("pageindex search requires authenticated caller")
	}
	storageProject := PageIndexStorageProject(auth, project)
	return p.searcher.Run(ctx, SearchInput{
		Project:    storageProject,
		Query:      query,
		PathPrefix: pathPrefix,
		Limit:      limit,
		ValidateSource: func(validateCtx context.Context, userPath, expectedHash string) (bool, error) {
			if expectedHash == "" {
				return false, nil
			}
			read, readErr := p.userFS.Read(validateCtx, auth, project, userPath, 0, -1)
			if readErr != nil {
				return false, errors.Wrap(readErr, "read pageindex source")
			}
			return files.HashFileContent([]byte(read.Content)) == expectedHash, nil
		},
	})
}

type indexedPathEntry struct {
	path  string
	entry IndexEntry
}

func (p *Plugin) indexEntriesForPath(ctx context.Context, storageProject, target string, recursive bool) ([]indexedPathEntry, error) {
	index, err := p.store.GetIndex(ctx, storageProject)
	if err != nil {
		return nil, err
	}
	prefix := strings.TrimSuffix(target, "/") + "/"
	entries := make([]indexedPathEntry, 0)
	for indexedPath, entry := range index {
		if indexedPath == target || (recursive && strings.HasPrefix(indexedPath, prefix)) {
			entries = append(entries, indexedPathEntry{path: indexedPath, entry: entry})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	return entries, nil
}

// PageIndexStorageProject namespaces the system catalog by tenant while keeping
// the user-visible project name unchanged. SystemFS is intentionally bound to a
// plugin-wide owner, so project names alone are insufficient for isolation.
func PageIndexStorageProject(auth files.AuthContext, project string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(auth.APIKeyHash))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(project))
	return "tenant-" + hex.EncodeToString(h.Sum(nil))
}

func isLongDocPath(path string) bool {
	p := strings.ToLower(strings.TrimSpace(path))
	return strings.HasSuffix(p, ".pdf") || strings.HasSuffix(p, ".md")
}

func docIDFromAuth(auth files.AuthContext, project, path string) string {
	h := sha256.New()
	h.Write([]byte(auth.APIKeyHash))
	h.Write([]byte{0})
	h.Write([]byte(project))
	h.Write([]byte{0})
	h.Write([]byte(path))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Compile-time assertion that Plugin satisfies the manager interface.
var _ mcpplugin.Plugin = (*Plugin)(nil)
