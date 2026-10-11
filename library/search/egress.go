package search

import (
	"context"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/library/toolpolicy"
	"github.com/Laisky/laisky-blog-graphql/library/crawleregress"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

// Egress configuration keys. Missing renderer evidence is rejected by default.
// An explicit compatibility opt-out is unverified and must not be represented as protection.
const (
	configKeyMaxRedirects      = "settings.mcp.tools.web_fetch.egress.max_redirects"
	configKeyAllowSubresources = "settings.mcp.tools.web_fetch.egress.allow_subresources"
	configKeyRequireVerified   = "settings.mcp.tools.web_fetch.egress.require_verified"

	// defaultMaxRedirects keeps document redirects bounded across all producers.
	defaultMaxRedirects = crawleregress.DefaultMaxRedirects
)

// EgressSettings is the crawl-side policy this deployment publishes.
type EgressSettings struct {
	// MaxRedirects bounds the hops the renderer may follow.
	MaxRedirects int
	// AllowSubresources permits page subresource loads.
	AllowSubresources bool
	// RequireVerified rejects a render result that carries no bound connection receipt.
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
	if !gconfig.Shared.IsSet(configKeyMaxRedirects) {
		settings.MaxRedirects = defaultMaxRedirects
	}
	if settings.MaxRedirects < 0 {
		settings.MaxRedirects = 0
	}
	if settings.MaxRedirects > crawleregress.MaxRedirects {
		settings.MaxRedirects = crawleregress.MaxRedirects
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

// verifyRenderedTask checks the immutable submitted target/policy against actual connection evidence.
func verifyRenderedTask(ctx context.Context, settings EgressSettings, taskID, target string, policy toolpolicy.EgressPolicy, task *rlibs.HTMLCrawlerTask) error {
	if task == nil || task.TaskID != taskID || task.Url != target || task.Egress == nil || crawleregress.PolicyDigest(target, *task.Egress) != crawleregress.PolicyDigest(target, policy) {
		return fetchFailure(crawleregress.ErrRejected, "crawler_egress_rejected", "verification", taskID)
	}
	err := crawleregress.VerifyReceipt(ctx, taskID, target, policy, task.EgressReceipt)
	if err == nil {
		return nil
	}
	if errors.Is(err, crawleregress.ErrUnverified) {
		if !settings.RequireVerified {
			return nil
		}
		return fetchFailure(err, "crawler_egress_unverified", "verification", taskID)
	}
	return fetchFailure(err, "crawler_egress_rejected", "verification", taskID)
}
