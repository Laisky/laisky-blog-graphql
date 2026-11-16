package mcp

import (
	"context"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/library"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
)

// consumeCall sends one one-api consume and reports, besides the billing
// error, what one-api's answer proves about the key (oneapi.ConsumeAt).
type consumeCall func(ctx context.Context, apiKey string, price oneapi.Price, reason string) (oneapi.ConsumeReceipt, error)

// consumeAt binds consumeCall to the one-api origin baseURL.
func consumeAt(baseURL string) consumeCall {
	return func(ctx context.Context, apiKey string, price oneapi.Price, reason string) (oneapi.ConsumeReceipt, error) {
		return oneapi.ConsumeAt(ctx, baseURL, apiKey, price, reason)
	}
}

// observeAuthorityAcceptance turns consume into the server's billing reporter.
// Every tool invocation already makes one consume call (a paid tool's charge,
// or the zero-cost usage report of every other tool), and one-api serves
// /api/token/consume behind the same TokenAuth as the validation endpoint. A
// consume whose answer proves acceptance (oneapi.ConsumeReceipt.TokenAccepted:
// 200, JSON, "success": true, the same proof validation requires) is a fresh
// authority verdict and restarts the key's trust period
// (mcpauth.Authenticator.BeginAuthorityCall). Active keys therefore need no
// separate revalidation call, and the gate's budgets only have to cover keys
// that call no tool within positive_ttl. Any other answer, or a consume under
// another base URL than the validator's authority, changes no verdict.
func observeAuthorityAcceptance(consume consumeCall, baseURL string, resolve func() *mcpauth.Authenticator) externalBillingReporter {
	return func(ctx context.Context, apiKey string, price oneapi.Price, reason string) error {
		done := resolve().BeginAuthorityCall(baseURL, library.StripBearerPrefix(apiKey))
		receipt, err := consume(ctx, apiKey, price, reason)
		done(err == nil && receipt.TokenAccepted)
		return err
	}
}
