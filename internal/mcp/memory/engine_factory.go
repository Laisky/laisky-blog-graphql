package memory

import (
	"strings"

	errors "github.com/Laisky/errors/v2"
	sdkmemory "github.com/Laisky/go-utils/v6/agents/memory"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// newEngineForAuth builds a memory engine bound to one authenticated storage adapter.
func (service *Service) newEngineForAuth(auth files.AuthContext) (*sdkmemory.StandardEngine, error) {
	adapter, err := newStorageAdapter(service.fileService, auth)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	config := sdkmemory.Config{
		RecentContextItems:     service.settings.RecentContextItems,
		RecallFactsLimit:       service.settings.RecallFactsLimit,
		SearchLimit:            service.settings.SearchLimit,
		CompactThreshold:       service.settings.CompactThreshold,
		L1RetentionDays:        service.settings.L1RetentionDays,
		L2RetentionDays:        service.settings.L2RetentionDays,
		CompactionMinAge:       service.settings.CompactionMinAge,
		SummaryRefreshInterval: service.settings.SummaryRefreshInterval,
		MaxProcessedTurns:      service.settings.MaxProcessedTurns,
		TimeNow:                service.clock,
	}

	if service.settings.Heuristic.Enabled {
		config.LLMModel = service.settings.Heuristic.Model
		config.LLMAPIBase = strings.TrimSpace(service.settings.Heuristic.BaseURL)
		config.LLMTimeout = service.settings.Heuristic.Timeout
		config.LLMMaxOutputTokens = service.settings.Heuristic.MaxOutputTokens
		config.LLMAPIKey = strings.TrimSpace(auth.APIKey)
	}

	engine, err := sdkmemory.NewEngine(adapter, config)
	if err != nil {
		return nil, errors.Wrap(err, "new memory engine")
	}

	return engine, nil
}
