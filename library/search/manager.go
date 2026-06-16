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
	// Type returns the engine type (e.g. "google", "serp_google").
	Type() string
	// Search executes the query and returns a slice of SearchResultItem when successful.
	Search(ctx context.Context, query string) ([]SearchResultItem, error)
}

// SearchOutput contains the search results along with metadata about the engine that produced them.
type SearchOutput struct {
	Items      []SearchResultItem
	EngineName string
	EngineType string
}

// Provider exposes the high level search capability used by resolvers and tools.
type Provider interface {
	// Search routes the query through the configured engines and returns the first successful result.
	Search(ctx context.Context, query string) (*SearchOutput, error)
}

// ManagerOption customizes a Manager during construction.
type ManagerOption func(*Manager)

// WithLogger overrides the fallback logger used when no contextual logger is available.
func WithLogger(logger logSDK.Logger) ManagerOption {
	return func(m *Manager) {
		if logger != nil {
			m.logger = logger
		}
	}
}

// WithRand supplies a deterministic random generator for initial round-robin offsets.
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
	nextByTier  []int
	selectorMu  sync.Mutex
	logger      logSDK.Logger
}

// NewManager constructs a Manager with the provided priority tiers.
// Each inner slice represents a tier where engines share equal priority.
// Engines within a tier are attempted in round-robin order.
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
	manager.initializeRoundRobinOffsets()

	return manager, nil
}

// Search routes the query through the configured engines until one succeeds or the constraints are exhausted.
func (m *Manager) Search(ctx context.Context, query string) (*SearchOutput, error) {
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
		engines := m.roundRobinEngines(tierIdx, tier)
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
			if err != nil {
				failureDetails = append(failureDetails, fmt.Sprintf("%s: %v", engine.Name(), err))
				if tierLogger != nil {
					tierLogger.Warn("search engine failed",
						zap.String("engine", engine.Name()),
						zap.Int("tier", tierIdx),
						zap.Int("attempt", attempts),
						zap.Error(err),
					)
				}
				continue
			}

			if tierLogger != nil {
				tierLogger.Info("search manager succeeded",
					zap.String("engine", engine.Name()),
					zap.String("engine_type", engine.Type()),
					zap.Int("tier", tierIdx),
					zap.Int("attempt", attempts),
				)
			}
			return &SearchOutput{
				Items:      items,
				EngineName: engine.Name(),
				EngineType: engine.Type(),
			}, nil
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

// initializeRoundRobinOffsets seeds each tier cursor before searches begin.
// It accepts no parameters and returns no values.
func (m *Manager) initializeRoundRobinOffsets() {
	m.nextByTier = make([]int, len(m.tiers))
	for tierIdx, tier := range m.tiers {
		if len(tier) <= 1 {
			continue
		}
		m.nextByTier[tierIdx] = m.rand.Intn(len(tier))
	}
}

// roundRobinEngines returns a rotated copy of tier and advances that tier's cursor.
// The tierIdx parameter selects which cursor to advance, and tier supplies the engines to order.
// The returned slice is safe for the caller to iterate without mutating manager state.
func (m *Manager) roundRobinEngines(tierIdx int, tier []Engine) []Engine {
	if len(tier) == 0 {
		return nil
	}

	m.selectorMu.Lock()
	defer m.selectorMu.Unlock()

	if tierIdx < 0 || tierIdx >= len(m.nextByTier) {
		cloned := make([]Engine, len(tier))
		copy(cloned, tier)
		return cloned
	}

	start := m.nextByTier[tierIdx] % len(tier)
	m.nextByTier[tierIdx] = (start + 1) % len(tier)

	ordered := make([]Engine, 0, len(tier))
	ordered = append(ordered, tier[start:]...)
	ordered = append(ordered, tier[:start]...)
	return ordered
}
