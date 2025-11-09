package search

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	appLog "github.com/Laisky/laisky-blog-graphql/library/log"
)

const (
	defaultMaxRetries         = 3
	defaultMaxDistinctEngines = 3
)

// Engine defines a concrete search backend that can process queries and return items.
type Engine interface {
	// Name returns the unique identifier for the engine instance.
	Name() string
	// Search executes the query and returns a slice of SearchResultItem when successful.
	Search(ctx context.Context, query string) ([]SearchResultItem, error)
}

// Provider exposes the high level search capability used by resolvers and tools.
type Provider interface {
	// Search routes the query through the configured engines and returns the first successful result.
	Search(ctx context.Context, query string) ([]SearchResultItem, error)
}

// ManagerOption customises a Manager during construction.
type ManagerOption func(*Manager)

// WithLogger overrides the fallback logger used when no contextual logger is available.
func WithLogger(logger logSDK.Logger) ManagerOption {
	return func(m *Manager) {
		if logger != nil {
			m.logger = logger
		}
	}
}

// WithRand supplies a deterministic random generator, primarily for testing.
func WithRand(r *rand.Rand) ManagerOption {
	return func(m *Manager) {
		if r != nil {
			m.rand = r
		}
	}
}

// WithMaxRetries adjusts how many retry attempts are permitted after the initial try.
func WithMaxRetries(retries int) ManagerOption {
	return func(m *Manager) {
		if retries >= 0 {
			m.maxRetries = retries
		}
	}
}

// WithMaxDistinctEngines limits how many unique engines can participate in a single query.
func WithMaxDistinctEngines(limit int) ManagerOption {
	return func(m *Manager) {
		if limit > 0 {
			m.maxDistinct = limit
		}
	}
}

// Manager orchestrates multiple search engines using priority tiers and failover policies.
type Manager struct {
	tiers       [][]Engine
	maxRetries  int
	maxDistinct int
	rand        *rand.Rand
	randMu      sync.Mutex
	logger      logSDK.Logger
}

// NewManager constructs a Manager with the provided priority tiers.
// Each inner slice represents a tier where engines share equal priority.
// Engines within a tier are attempted in random order.
func NewManager(tiers [][]Engine, opts ...ManagerOption) (*Manager, error) {
	filtered := make([][]Engine, 0, len(tiers))
	totalEngines := 0
	for _, tier := range tiers {
		cleanedTier := make([]Engine, 0, len(tier))
		for _, engine := range tier {
			if engine == nil {
				continue
			}
			cleanedTier = append(cleanedTier, engine)
		}
		if len(cleanedTier) == 0 {
			continue
		}
		totalEngines += len(cleanedTier)
		tierCopy := make([]Engine, len(cleanedTier))
		copy(tierCopy, cleanedTier)
		filtered = append(filtered, tierCopy)
	}

	if totalEngines == 0 {
		return nil, errors.New("search manager requires at least one engine")
	}

	manager := &Manager{
		tiers:       filtered,
		maxRetries:  defaultMaxRetries,
		maxDistinct: defaultMaxDistinctEngines,
		rand:        rand.New(rand.NewSource(time.Now().UnixNano())),
		logger:      appLog.Logger.Named("search_manager"),
	}

	for _, opt := range opts {
		opt(manager)
	}

	return manager, nil
}

// Search routes the query through the configured engines until one succeeds or the constraints are exhausted.
func (m *Manager) Search(ctx context.Context, query string) ([]SearchResultItem, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return nil, errors.New("search query cannot be empty")
	}

	logger := m.logger
	if ctx != nil {
		if ctxLogger := gmw.GetLogger(ctx); ctxLogger != nil {
			logger = ctxLogger.Named("search_manager").With(zap.String("query", trimmed))
		} else if logger != nil {
			logger = logger.With(zap.String("query", trimmed))
		}
	}

	allowedAttempts := m.maxRetries + 1
	if allowedAttempts <= 0 {
		allowedAttempts = 1
	}

	if m.maxDistinct > 0 && allowedAttempts > m.maxDistinct {
		allowedAttempts = m.maxDistinct
	}

	totalAvailable := 0
	for _, tier := range m.tiers {
		totalAvailable += len(tier)
	}
	if allowedAttempts > totalAvailable {
		allowedAttempts = totalAvailable
	}

	attempts := 0
	usedEngines := map[string]struct{}{}
	var failureDetails []string

	for tierIdx, tier := range m.tiers {
		engines := m.shuffleEngines(tier)
		for _, engine := range engines {
			if attempts >= allowedAttempts {
				break
			}

			key := fmt.Sprintf("%T:%p", engine, engine)
			if _, exists := usedEngines[key]; exists {
				continue
			}
			usedEngines[key] = struct{}{}
			attempts++

			tierLogger := logger
			if tierLogger != nil {
				tierLogger.Debug("search manager invoking engine",
					zap.String("engine", engine.Name()),
					zap.Int("tier", tierIdx),
					zap.Int("attempt", attempts),
				)
			}

			items, err := engine.Search(ctx, trimmed)
			if err == nil {
				if tierLogger != nil {
					tierLogger.Info("search manager succeeded",
						zap.String("engine", engine.Name()),
						zap.Int("tier", tierIdx),
						zap.Int("attempt", attempts),
					)
				}
				return items, nil
			}

			failureDetails = append(failureDetails, fmt.Sprintf("%s: %v", engine.Name(), err))
			if tierLogger != nil {
				tierLogger.Warn("search engine failed",
					zap.String("engine", engine.Name()),
					zap.Int("tier", tierIdx),
					zap.Int("attempt", attempts),
					zap.Error(err),
				)
			}
		}
		if attempts >= allowedAttempts {
			break
		}
	}

	if len(failureDetails) == 0 {
		return nil, errors.New("search manager could not locate a usable engine")
	}

	message := fmt.Sprintf("search manager exhausted engines after %d attempt(s): %s", attempts, strings.Join(failureDetails, "; "))
	return nil, errors.New(message)
}

func (m *Manager) shuffleEngines(tier []Engine) []Engine {
	// shuffleEngines returns a randomly permuted copy of the tier to balance
	// load across peers that share the same priority level.
	if len(tier) <= 1 {
		return tier
	}

	cloned := make([]Engine, len(tier))
	copy(cloned, tier)

	m.randMu.Lock()
	m.rand.Shuffle(len(cloned), func(i, j int) {
		cloned[i], cloned[j] = cloned[j], cloned[i]
	})
	m.randMu.Unlock()

	return cloned
}
