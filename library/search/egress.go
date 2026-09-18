package search

import (
	"context"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/library/toolpolicy"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

// Egress configuration keys. Missing renderer evidence is rejected by default.
// An operator can explicitly opt out for a legacy renderer, but that mode is
// unverified and must not be represented as egress protection.
const (
	configKeyMaxRedirects      = "settings.mcp.tools.web_fetch.egress.max_redirects"
	configKeyAllowSubresources = "settings.mcp.tools.web_fetch.egress.allow_subresources"
	configKeyRequireVerified   = "settings.mcp.tools.web_fetch.egress.require_verified"

	// defaultMaxRedirects matches ordinary browser behavior for a document
	// load while keeping the chain short enough to re-admit cheaply.
	defaultMaxRedirects = 5
)

// EgressSettings is the crawl-side policy this deployment publishes.
type EgressSettings struct {
	// MaxRedirects bounds the hops the renderer may follow.
	MaxRedirects int
	// AllowSubresources permits page subresource loads.
	AllowSubresources bool
	// RequireVerified rejects a render result that carries no request chain.
	// It defaults to true. An explicit false permits unverified legacy results
	// and is an unsafe compatibility choice, not proof of a safe crawl.
	RequireVerified bool
}

// LoadEgressSettings reads the crawl egress policy from configuration.
func LoadEgressSettings() EgressSettings {
	settings := EgressSettings{
		MaxRedirects:      gconfig.Shared.GetInt(configKeyMaxRedirects),
		AllowSubresources: gconfig.Shared.GetBool(configKeyAllowSubresources),
		RequireVerified:   true,
	}
	if gconfig.Shared.IsSet(configKeyRequireVerified) {
		settings.RequireVerified = gconfig.Shared.GetBool(configKeyRequireVerified)
	}
	if settings.MaxRedirects <= 0 {
		settings.MaxRedirects = defaultMaxRedirects
	}
	return settings
}

// crawlerEgressPolicy converts the shared policy into the task envelope shape.
func crawlerEgressPolicy(policy toolpolicy.EgressPolicy) *rlibs.CrawlerEgressPolicy {
	return &rlibs.CrawlerEgressPolicy{
		Host:              policy.Host,
		Addresses:         policy.SortedAddresses(),
		MaxRedirects:      policy.MaxRedirects,
		AllowSubresources: policy.AllowSubresources,
	}
}

// verifyRenderedEgress re-admits the origins the renderer says it contacted.
//
// A policy violation always fails the fetch: a redirect into a private address
// or a rebound host must not return a body to the caller. An absent chain is a
// different condition — nothing was checked — and fails under the secure
// default. Only an explicit unsafe compatibility setting may accept it.
func verifyRenderedEgress(ctx context.Context, logger logSDK.Logger, settings EgressSettings,
	policy toolpolicy.EgressPolicy, chain []string,
) error {
	err := toolpolicy.VerifyEgressChain(ctx, policy, chain)
	switch {
	case err == nil:
		logger.Debug("renderer egress verified", zap.Int("hops", len(chain)))
		return nil
	case errors.Is(err, toolpolicy.ErrEgressUnverified):
		if settings.RequireVerified {
			return errors.Wrap(err, "verify renderer egress")
		}
		logger.Warn("renderer reported no request chain; egress is unverified",
			zap.String("host", policy.Host), zap.Bool("require_verified", false))
		return nil
	default:
		return errors.Wrap(err, "renderer violated the egress policy")
	}
}
