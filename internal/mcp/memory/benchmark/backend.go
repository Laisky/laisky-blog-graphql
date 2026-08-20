package benchmark

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	errors "github.com/Laisky/errors/v2"
	_ "github.com/mattn/go-sqlite3"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
	pageindexplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/pageindex"
	ragplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/rag"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// Backend is the minimal production-shaped surface measured by Runner.
type Backend interface {
	Name() string
	Write(ctx context.Context, project string, document Document) error
	Search(ctx context.Context, project string, query Query, limit int) ([]SearchHit, error)
	Delete(ctx context.Context, project, path string, recursive bool) error
	Close(ctx context.Context) error
}

// PluginBackend adapts an in-process production memory plugin to Backend.
type PluginBackend struct {
	plugin mcpplugin.Plugin
	auth   files.AuthContext
	name   string
}

// NewPluginBackend constructs an adapter around a real memory plugin.
func NewPluginBackend(plugin mcpplugin.Plugin, auth files.AuthContext, backendName string) (*PluginBackend, error) {
	if plugin == nil {
		return nil, errors.New("memory plugin is nil")
	}
	if strings.TrimSpace(auth.APIKeyHash) == "" {
		return nil, errors.New("benchmark auth api key hash is required")
	}
	if strings.TrimSpace(backendName) == "" {
		backendName = "plugin"
	}
	return &PluginBackend{plugin: plugin, auth: auth, name: backendName}, nil
}

// Name returns the backend mode written to run metadata.
func (b *PluginBackend) Name() string { return b.name }

// Write stores one benchmark document through the plugin contract.
func (b *PluginBackend) Write(ctx context.Context, project string, document Document) error {
	encoding := document.ContentEncoding
	if encoding == "" {
		encoding = "utf-8"
	}
	_, err := b.plugin.Write(ctx, b.auth, project, document.Path, document.Content, encoding, 0, files.WriteModeTruncate)
	if err != nil {
		return errors.Wrapf(err, "plugin %s write %s", b.plugin.Name(), document.Path)
	}
	return nil
}

// Search executes one query through the plugin contract.
func (b *PluginBackend) Search(ctx context.Context, project string, query Query, limit int) ([]SearchHit, error) {
	result, err := b.plugin.Search(ctx, b.auth, project, query.Text, query.PathPrefix, limit)
	if err != nil {
		return nil, errors.Wrapf(err, "plugin %s search %s", b.plugin.Name(), query.ID)
	}
	hits := make([]SearchHit, 0, len(result.Chunks))
	for _, chunk := range result.Chunks {
		hits = append(hits, SearchHit{
			Project: chunk.Project, FilePath: chunk.FilePath, SeekStart: chunk.FileSeekStartBytes,
			SeekEnd: chunk.FileSeekEndBytes, IsFullFile: chunk.IsFullFile,
			Content: chunk.ChunkContent, Score: chunk.Score,
		})
	}
	return hits, nil
}

// Delete removes a benchmark path through the plugin contract.
func (b *PluginBackend) Delete(ctx context.Context, project, path string, recursive bool) error {
	_, err := b.plugin.Delete(ctx, b.auth, project, path, recursive)
	if err != nil {
		return errors.Wrapf(err, "plugin %s delete %s", b.plugin.Name(), path)
	}
	return nil
}

// Close stops the adapted plugin.
func (b *PluginBackend) Close(ctx context.Context) error {
	return errors.WithStack(b.plugin.Stop(ctx))
}

// MCPBackend benchmarks a deployed plugin through the public MCP file tools.
type MCPBackend struct {
	client *MCPClient
	plugin string
}

// NewMCPBackend initializes an MCP session and returns a benchmark backend.
func NewMCPBackend(ctx context.Context, client *MCPClient, plugin string) (*MCPBackend, error) {
	if client == nil {
		return nil, errors.New("MCP client is nil")
	}
	plugin = strings.ToLower(strings.TrimSpace(plugin))
	if plugin != mcpplugin.DefaultPluginRAG && plugin != mcpplugin.DefaultPluginPageIndex {
		return nil, errors.Errorf("unsupported memory plugin %q", plugin)
	}
	if err := client.Initialize(ctx); err != nil {
		return nil, errors.Wrap(err, "initialize MCP benchmark session")
	}
	return &MCPBackend{client: client, plugin: plugin}, nil
}

// Name returns the deployed MCP backend identifier.
func (b *MCPBackend) Name() string { return "mcp" }

// Write calls the public file_write MCP tool.
func (b *MCPBackend) Write(ctx context.Context, project string, document Document) error {
	encoding := document.ContentEncoding
	if encoding == "" {
		encoding = "utf-8"
	}
	_, err := b.client.CallTool(ctx, "file_write", map[string]any{
		"project": project, "path": document.Path, "content": document.Content,
		"content_encoding": encoding, "mode": string(files.WriteModeTruncate), "plugin": b.plugin,
	})
	return errors.Wrapf(err, "MCP file_write %s", document.Path)
}

// Search calls the public file_search MCP tool.
func (b *MCPBackend) Search(ctx context.Context, project string, query Query, limit int) ([]SearchHit, error) {
	payload, err := b.client.CallTool(ctx, "file_search", map[string]any{
		"project": project, "query": query.Text, "path_prefix": query.PathPrefix,
		"limit": limit, "plugin": b.plugin,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "MCP file_search %s", query.ID)
	}
	var response struct {
		Chunks []struct {
			Project    string  `json:"project"`
			FilePath   string  `json:"file_path"`
			SeekStart  int64   `json:"file_seek_start_bytes"`
			SeekEnd    int64   `json:"file_seek_end_bytes"`
			IsFullFile bool    `json:"is_full_file"`
			Content    string  `json:"chunk_content"`
			Score      float64 `json:"score"`
		} `json:"chunks"`
	}
	if err := decodeToolPayload(payload, &response); err != nil {
		return nil, errors.Wrap(err, "decode file_search result")
	}
	hits := make([]SearchHit, 0, len(response.Chunks))
	for _, chunk := range response.Chunks {
		hits = append(hits, SearchHit{
			Project: chunk.Project, FilePath: chunk.FilePath, SeekStart: chunk.SeekStart,
			SeekEnd: chunk.SeekEnd, IsFullFile: chunk.IsFullFile, Content: chunk.Content, Score: chunk.Score,
		})
	}
	return hits, nil
}

// Delete calls the public file_delete MCP tool.
func (b *MCPBackend) Delete(ctx context.Context, project, path string, recursive bool) error {
	_, err := b.client.CallTool(ctx, "file_delete", map[string]any{
		"project": project, "path": path, "recursive": recursive, "plugin": b.plugin,
	})
	return errors.Wrapf(err, "MCP file_delete %s", path)
}

// Close terminates the MCP session.
func (b *MCPBackend) Close(ctx context.Context) error { return b.client.Close(ctx) }

type localPluginBackend struct {
	*PluginBackend
	db *sql.DB
}

// NewLocalPluginBackend constructs the current production plugin against an isolated SQLite file service.
// External model calls are disabled: RAG uses the service's lexical/raw fallback, while PageIndex uses
// the real indexer/search loop with a deterministic StubLLM. This mode is for reproducible CI and local
// regression smoke tests, not a substitute for the scheduled deployed benchmark.
func NewLocalPluginBackend(ctx context.Context, pluginName string) (Backend, error) {
	pluginName = strings.ToLower(strings.TrimSpace(pluginName))
	dsn := fmt.Sprintf("file:memory-benchmark-%d?mode=memory&cache=shared&_foreign_keys=on", time.Now().UnixNano())
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, errors.Wrap(err, "open benchmark sqlite database")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	fileSettings := localFileSettings()
	credentialProtector, err := files.NewCredentialProtector(fileSettings.Security)
	if err != nil {
		_ = db.Close()
		return nil, errors.Wrap(err, "construct local benchmark credential protector")
	}
	credentialStore := newLocalCredentialStore()
	fileService, err := files.NewService(db, fileSettings, nil, nil, credentialProtector, credentialStore, nil, nil, nil)
	if err != nil {
		_ = db.Close()
		return nil, errors.Wrap(err, "construct local file service")
	}
	auth := files.AuthContext{
		APIKey: "memory-benchmark-local", APIKeyHash: "memory-benchmark-local",
		UserIdentity: "user:memory-benchmark-local",
	}

	var plugin mcpplugin.Plugin
	switch pluginName {
	case mcpplugin.DefaultPluginRAG:
		plugin, err = ragplugin.New(fileService)
	case mcpplugin.DefaultPluginPageIndex:
		plugin, err = newLocalPageIndex(fileService)
	default:
		err = errors.Errorf("unsupported local memory plugin %q", pluginName)
	}
	if err != nil {
		_ = db.Close()
		return nil, errors.WithStack(err)
	}
	adapter, err := NewPluginBackend(plugin, auth, "local-in-process")
	if err != nil {
		_ = plugin.Stop(ctx)
		_ = db.Close()
		return nil, err
	}
	return &localPluginBackend{PluginBackend: adapter, db: db}, nil
}

func (b *localPluginBackend) Close(ctx context.Context) error {
	pluginErr := b.PluginBackend.Close(ctx)
	dbErr := b.db.Close()
	if pluginErr != nil {
		return pluginErr
	}
	return errors.WithStack(dbErr)
}

func newLocalPageIndex(fileService *files.Service) (mcpplugin.Plugin, error) {
	systemFS, err := fileService.SystemNamespace("pageindex")
	if err != nil {
		return nil, errors.Wrap(err, "construct pageindex system namespace")
	}
	settings := pageindexplugin.Settings{
		Indexer: pageindexplugin.IndexerSettings{
			TimeoutIndex: 30 * time.Second, TimeoutQuery: 5 * time.Second, MaxConcurrency: 2,
			Retry: pageindexplugin.RetrySettings{MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
			Cache: pageindexplugin.CacheSettings{Enabled: false},
		},
		LLM: pageindexplugin.LLMSettings{
			IndexingModel: "gpt-5.4-mini", RetrieveModel: "gpt-5.4-mini", APIKey: "deterministic-local-stub",
		},
		Algo: pageindexplugin.AlgoSettings{
			TocCheckPageNum: 20, MaxPageNumEachNode: 10, MaxTokenNumEachNode: 20_000,
			GenerateNodeSummary: false, GenerateDocDescription: false,
		},
		TreeQuery: pageindexplugin.TreeQuerySettings{MaxSteps: 16, MaxTokens: 20_000, CandidateDocs: 16},
		PDF:       pageindexplugin.PDFSettings{TextParser: "pdfcpu", OutlineParser: "pdfcpu"},
	}
	tokenizer, err := pageindexplugin.NewTokenizer(settings.LLM.IndexingModel)
	if err != nil {
		return nil, errors.Wrap(err, "construct pageindex tokenizer")
	}
	llm := pageindexplugin.NewStubLLM()
	llm.SetDefault(&pageindexplugin.Response{
		Text:  `{"ranges":[{"start":1,"end":1000,"reason":"deterministic benchmark traversal"}]}`,
		Usage: pageindexplugin.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	})
	plugin, err := pageindexplugin.New(pageindexplugin.PluginDeps{
		UserFS: fileService, SystemFS: systemFS, Settings: settings, LLM: llm, Tokenizer: tokenizer,
		Logger: log.Logger.Named("memory_benchmark_pageindex"),
	})
	if err != nil {
		return nil, errors.Wrap(err, "construct pageindex plugin")
	}
	if err := plugin.Start(context.Background()); err != nil {
		return nil, errors.Wrap(err, "start pageindex plugin")
	}
	return plugin, nil
}

type localCredentialStore struct {
	mu     sync.Mutex
	values map[string]string
}

func newLocalCredentialStore() *localCredentialStore {
	return &localCredentialStore{values: make(map[string]string)}
}

func (s *localCredentialStore) Store(ctx context.Context, key, payload string, _ time.Duration) error {
	if err := ctx.Err(); err != nil {
		return errors.WithStack(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = payload
	return nil
}

func (s *localCredentialStore) Load(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", errors.WithStack(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	payload, ok := s.values[key]
	if !ok {
		return "", errors.New("local benchmark credential envelope not found")
	}
	return payload, nil
}

func (s *localCredentialStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return errors.WithStack(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}

func localFileSettings() files.Settings {
	return files.Settings{
		AllowRootWipe: false, MaxPayloadBytes: 2_000_000, MaxFileBytes: 10_000_000,
		MaxProjectBytes: 100_000_000, ListLimitDefault: 256, ListLimitMax: 1024,
		LockTimeout: 3 * time.Second, DeleteRetention: 24 * time.Hour,
		Search: files.SearchSettings{
			Enabled: true, LimitDefault: 20, LimitMax: 100, VectorCandidates: 100,
			LexicalCandidates: 100, SemanticWeight: 0.65, LexicalWeight: 0.35,
		},
		Index: files.IndexSettings{
			Workers: 1, BatchSize: 10, RetryMax: 0, RetryBackoff: 10 * time.Millisecond,
			ChunkBytes: 4096, FreshnessSLO: 2 * time.Second,
		},
		Security: files.SecuritySettings{
			EncryptionKEKs:        map[uint16]string{1: "memory-benchmark-local-encryption-key-2026"},
			CredentialCachePrefix: "memory-benchmark:credential",
			CredentialCacheTTL:    time.Hour,
		},
	}
}
