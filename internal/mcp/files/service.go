package files

import (
	"context"
	"errors"
	"time"

	errorsx "github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
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

// CredentialStore persists encrypted credential envelopes.
type CredentialStore interface {
	Store(ctx context.Context, key, payload string, ttl time.Duration) error
	Load(ctx context.Context, key string) (string, error)
	Delete(ctx context.Context, key string) error
}

// Service coordinates FileIO operations and indexing.
type Service struct {
	db             *gorm.DB
	settings       Settings
	logger         logSDK.Logger
	embedder       Embedder
	contextualizer Contextualizer
	rerank         RerankClient
	chunker        Chunker
	credential     *CredentialProtector
	credStore      CredentialStore
	lockProvider   LockProvider
	clock          Clock
}

// NewService constructs a FileIO service and runs migrations.
func NewService(db *gorm.DB, settings Settings, embedder Embedder, rerank RerankClient, credential *CredentialProtector, store CredentialStore, logger logSDK.Logger, lockProvider LockProvider, clock Clock) (*Service, error) {
	if db == nil {
		return nil, errorsx.New("gorm db is required")
	}
	if logger == nil {
		logger = log.Logger.Named("mcp_files_service")
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
			return nil, errorsx.Wrap(err, "init credential protector")
		}
	}

	if err := RunMigrations(context.Background(), db, logger); err != nil {
		return nil, errorsx.WithStack(err)
	}

	svc := &Service{
		db:             db,
		settings:       settings,
		logger:         logger,
		embedder:       embedder,
		contextualizer: NewOpenAIContextualizer(settings.Index.SummaryBaseURL, settings.Index.SummaryModel, settings.Index.SummaryTimeout, nil),
		rerank:         rerank,
		chunker:        DefaultChunker{MaxBytes: settings.Index.ChunkBytes},
		credential:     credential,
		credStore:      store,
		lockProvider:   lockProvider,
		clock:          clock,
	}

	return svc, nil
}

// LoggerFromContext returns the request-scoped logger when available.
func (s *Service) LoggerFromContext(ctx context.Context) logSDK.Logger {
	if ctx != nil {
		if ctxLogger := gmw.GetLogger(ctx); ctxLogger != nil {
			return ctxLogger
		}
		if ctxLogger, ok := ctx.Value(ctxkeys.Logger).(logSDK.Logger); ok && ctxLogger != nil {
			return ctxLogger
		}
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

// wrapServiceError adds stack context without logging.
func wrapServiceError(err error, message string) error {
	if err == nil {
		return nil
	}
	return errorsx.Wrap(err, message)
}

// warnOnError logs an error when needed for diagnostics.
func (s *Service) warnOnError(ctx context.Context, err error, msg string, fields ...zap.Field) {
	if err == nil {
		return
	}
	logger := s.LoggerFromContext(ctx)
	logger.Warn(msg, append(fields, zap.Error(err))...)
}

// isContextDone reports whether the context has been cancelled or exceeded its deadline.
func isContextDone(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	err := ctx.Err()
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
