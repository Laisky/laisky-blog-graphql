package files

import (
	"context"
	"database/sql"
	"sync/atomic"
	"time"

	errors "github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/pgvector/pgvector-go"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/library/db/sql/kv"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// Clock returns the current time in UTC.
type Clock func() time.Time

// Embedder converts text into vector embeddings.
type Embedder interface {
	EmbedTexts(ctx context.Context, apiKey string, inputs []string) ([]pgvector.Vector, error)
}

// Contextualizer generates short chunk-level context from whole-document information.
type Contextualizer interface {
	GenerateChunkContexts(ctx context.Context, apiKey, wholeDocument string, chunks []Chunk) ([]string, error)
}

// RerankClient orders documents by relevance.
type RerankClient interface {
	Rerank(ctx context.Context, apiKey, query string, docs []string) ([]float64, error)
}

// Service coordinates FileIO operations and indexing.
type Service struct {
	db             *sql.DB
	isPostgres     bool
	settings       Settings
	logger         logSDK.Logger
	embedder       Embedder
	contextualizer Contextualizer
	summarizer     FileSummarizer
	rerank         RerankClient
	chunker        Chunker
	credential     *CredentialProtector
	// credentials holds encrypted credential envelopes in the FileIO database,
	// so they commit or roll back with the file rows and outbox jobs.
	credentials *kv.Kv
	// nextCredentialPurge is the UnixNano time before which index workers
	// skip the expired-envelope sweep.
	nextCredentialPurge atomic.Int64
	lockProvider        LockProvider
	clock               Clock
}

// NewService constructs a FileIO service and runs migrations.
func NewService(db *sql.DB, settings Settings, embedder Embedder, rerank RerankClient, credential *CredentialProtector, logger logSDK.Logger, lockProvider LockProvider, clock Clock) (*Service, error) {
	return NewServiceWithContext(context.Background(), db, settings, embedder, rerank, credential, logger, lockProvider, clock)
}

// NewServiceWithContext constructs a FileIO service and runs migrations within the caller's lifetime.
func NewServiceWithContext(ctx context.Context, db *sql.DB, settings Settings, embedder Embedder, rerank RerankClient,
	credential *CredentialProtector, logger logSDK.Logger, lockProvider LockProvider, clock Clock,
) (*Service, error) {
	if db == nil {
		return nil, errors.New("sql db is required")
	}
	if logger == nil {
		logger = log.Logger.Named("mcp_files_service")
	}
	if settings.Security.CredentialCacheTTL <= 0 {
		settings.Security.CredentialCacheTTL = defaultCredentialCacheTTL
	}
	if lockProvider == nil {
		lockProvider = DefaultLockProvider{}
	}
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	if credential == nil && len(settings.Security.KEKs()) > 0 {
		var err error
		credential, err = NewCredentialProtector(settings.Security)
		if err != nil {
			return nil, errors.Wrap(err, "init credential protector")
		}
	}

	if err := RunMigrations(ctx, db, logger); err != nil {
		return nil, errors.WithStack(err)
	}

	isPostgres, err := detectPostgresDialect(ctx, db)
	if err != nil {
		return nil, errors.Wrap(err, "detect database dialect")
	}
	if settings.Search.Enabled && credential == nil {
		logger.Warn("file search has no credential encryption keys; indexing cannot reuse caller credentials and stays lexical")
	}

	svc := &Service{
		db:             db,
		isPostgres:     isPostgres,
		settings:       settings,
		logger:         logger,
		embedder:       embedder,
		contextualizer: NewOpenAIContextualizer(settings.Index.SummaryBaseURL, settings.Index.SummaryModel, settings.Index.SummaryTimeout, nil),
		rerank:         rerank,
		chunker:        DefaultChunker{MaxBytes: settings.Index.ChunkBytes},
		credential:     credential,
		lockProvider:   lockProvider,
		clock:          clock,
	}
	if settings.Index.FileSummary.Enabled {
		svc.summarizer = NewOpenAIFileSummarizer(settings.Index.FileSummary, nil)
	}
	// Envelopes expire on the service clock, read at call time.
	if svc.credentials, err = openCredentialEnvelopes(db, func() time.Time { return svc.clock() }); err != nil {
		return nil, errors.WithStack(err)
	}

	return svc, nil
}

// LoggerFromContext returns the request-scoped logger when available.
func (s *Service) LoggerFromContext(ctx context.Context) logSDK.Logger {
	if ctx != nil {
		if ctxLogger, ok := ctx.Value(ctxkeys.Logger).(logSDK.Logger); ok && ctxLogger != nil {
			return ctxLogger
		}
		return gmw.GetLogger(ctx)
	}
	if s != nil && s.logger != nil {
		return s.logger
	}
	return log.Logger.Named("mcp_files_fallback")
}

// validateAuth ensures the auth context is present.
func (s *Service) validateAuth(auth AuthContext) error {
	if auth.APIKeyHash == "" {
		return NewError(ErrCodePermissionDenied, "missing api key", false)
	}
	return nil
}

// isContextDone reports whether the context has been canceled or exceeded its deadline.
func isContextDone(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	err := ctx.Err()
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
