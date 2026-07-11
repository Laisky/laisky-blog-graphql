package pageindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// Write forwards to userFS.WriteWith with SkipRAGIndex=true and triggers indexing.
// Indexing and summarization always run against the complete post-write file re-read
// from storage, never merely the write argument, so APPEND/OVERWRITE/TRUNCATE describe
// the whole content (§4.5). Every stored file — including unsupported extensions —
// receives a bounded user-row summary published through the conditional publisher.
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
		if p.log != nil {
			p.log.Warn("pageindex.write reread content: " + readErr.Error())
		}
		return res, nil
	}
	fullBytes := []byte(full.Content)
	sourceHash := files.HashFileContent(fullBytes)

	if !isLongDocPath(path) || p.indexer == nil {
		// Unsupported extension (or no indexer): store a local deterministic summary
		// without routing through RAG or exposing a false search hit.
		desc := files.DeterministicFileSummaryFallback(full.Content, docSummaryMaxWords, docSummaryMaxBytes)
		p.publishUserRowSummary(ctx, auth, project, path, desc, files.SummarySourceDeterministicFallback, sourceHash)
		return res, nil
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
		p.publishUserRowSummary(ctx, auth, project, path, desc, files.SummarySourceDeterministicFallback, sourceHash)
		return res, nil
	}

	tree.SourceContentHash = sourceHash
	desc, source := finalizeDocDescription(tree, fullBytes)
	tree.DocDescription = desc

	// Publish the user-row summary first, guarded by the content hash. A false result
	// means a newer generation is already active, so this stale writer must not
	// overwrite the tree or index mapping (P06).
	published := p.publishUserRowSummary(ctx, auth, project, path, desc, source, sourceHash)
	if !published {
		if p.log != nil {
			p.log.Debug("pageindex.write skipped stale tree publish: " + path)
		}
		return res, nil
	}

	if err := p.store.PutTree(ctx, project, docID, tree); err != nil {
		if p.log != nil {
			p.log.Warn("pageindex.write put tree: " + err.Error())
		}
		return res, nil
	}
	entry := IndexEntry{
		DocID:             docID,
		Type:              string(kind),
		PageCount:         tree.PageCount,
		LineCount:         tree.LineCount,
		IndexedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		SourceContentHash: sourceHash,
	}
	if err := p.store.UpdateIndexEntry(ctx, project, path, entry); err != nil && p.log != nil {
		p.log.Warn("pageindex.write update index: " + err.Error())
	}
	return res, nil
}

// publishUserRowSummary publishes bounded summary catalog metadata onto the user file
// row through the conditional publisher. It returns whether the summary was published
// (false when a newer content generation is already active).
func (p *Plugin) publishUserRowSummary(ctx context.Context, auth files.AuthContext, project, path, summary string, source files.SummarySource, contentHash string) bool {
	status := files.SummaryStatusReady
	if source == files.SummarySourceDeterministicFallback {
		status = files.SummaryStatusDegraded
	}
	published, err := p.userFS.PublishPluginSummary(ctx, auth, project, path, files.PluginSummaryInput{
		ExpectedContentHash: contentHash,
		Summary:             summary,
		WordCount:           files.SummaryWordCount(summary),
		Source:              source,
		Status:              status,
		Model:               "",
		PromptVersion:       AlgorithmVersion,
		GenerationKey:       contentHash,
	})
	if err != nil && p.log != nil {
		p.log.Warn("pageindex.write publish summary: " + err.Error())
	}
	return published
}

// Delete forwards to userFS and cleans up sysstore entries.
func (p *Plugin) Delete(ctx context.Context, auth files.AuthContext, project, path string, recursive bool) (files.DeleteResult, error) {
	res, err := p.userFS.Delete(ctx, auth, project, path, recursive)
	if err != nil {
		return res, err
	}
	if entry, ok, _ := p.store.RemoveIndexEntry(ctx, project, path); ok {
		_ = p.store.DeleteTree(ctx, project, entry.DocID)
	}
	return res, nil
}

// Rename updates both the user FS row and the index mapping (tree JSON unchanged).
func (p *Plugin) Rename(ctx context.Context, auth files.AuthContext, project, src, dst string, overwrite bool) (files.RenameResult, error) {
	res, err := p.userFS.Rename(ctx, auth, project, src, dst, overwrite)
	if err != nil {
		return res, err
	}
	_ = p.store.RenameIndexEntry(ctx, project, src, dst)
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
	_ = auth // auth is enforced by the SystemFS handle scoped at construction
	return p.searcher.Run(ctx, SearchInput{Project: project, Query: query, PathPrefix: pathPrefix, Limit: limit})
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
