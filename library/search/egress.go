package search

import (
	"context"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/library/toolpolicy"
	"github.com/Laisky/laisky-blog-graphql/library/crawleregress"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

// Egress configuration keys control request policy; receipt verification is mandatory.
const (
	configKeyMaxRedirects      = "settings.mcp.tools.web_fetch.egress.max_redirects"
	configKeyAllowSubresources = "settings.mcp.tools.web_fetch.egress.allow_subresources"

	// defaultMaxRedirects keeps document redirects bounded across all producers.
	defaultMaxRedirects = crawleregress.DefaultMaxRedirects
)

// EgressSettings is the crawl-side policy this deployment publishes.
type EgressSettings struct {
	// MaxRedirects bounds the hops the renderer may follow.
	MaxRedirects int
	// AllowSubresources permits page subresource loads.
	AllowSubresources bool
	// RequireVerified remains true in loaded settings for source compatibility.
	// Deprecated: Verification is mandatory; a caller-supplied false is ignored.
	RequireVerified bool
}

// LoadEgressSettings reads the crawl egress policy from configuration.
func LoadEgressSettings() EgressSettings {
	settings := EgressSettings{
		MaxRedirects:      gconfig.Shared.GetInt(configKeyMaxRedirects),
		AllowSubresources: gconfig.Shared.GetBool(configKeyAllowSubresources),
		RequireVerified:   true,
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

// verifyRenderedTask always checks the submitted identity, policy and connection
// receipt. The retained settings parameter cannot waive missing evidence.
func verifyRenderedTask(ctx context.Context, _ EgressSettings, taskID, target string, policy toolpolicy.EgressPolicy, task *rlibs.HTMLCrawlerTask) error {
	if task == nil || task.TaskID != taskID || task.Url != target || task.Egress == nil || crawleregress.PolicyDigest(target, *task.Egress) != crawleregress.PolicyDigest(target, policy) {
		return fetchFailure(crawleregress.ErrRejected, "crawler_egress_rejected", "verification", taskID)
	}
	err := crawleregress.VerifyReceipt(ctx, taskID, target, policy, task.EgressReceipt)
	if err == nil {
		return nil
	}
	if errors.Is(err, crawleregress.ErrUnverified) {
		return fetchFailure(err, "crawler_egress_unverified", "verification", taskID)
	}
	return fetchFailure(err, "crawler_egress_rejected", "verification", taskID)
}
