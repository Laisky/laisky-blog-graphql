package mcp

import (
	"context"
	"math"
	"net/http"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/library/httpbody"
)

// In-flight body budgets of the MCP transport.
//
// Every POST body the transport serves is held in memory while its request
// runs, so the bytes in-flight requests hold together are bounded, separately
// for two classes of callers:
//
//   - verified: requests whose API key the credential gate verified. Their
//     budget is RequestBodyBudget.MaxInflightBytes, and one key (its
//     APIKeyHash) may hold at most max(MaxBytes, capacity/4) of it, so one
//     caller cannot starve the others.
//   - anonymous: requests without a credential (discovery only), bounded by
//     RequestBodyBudget.AnonymousMaxInflightBytes without a per-caller share.
//
// The body budget wrapper (withRequestBodyBudget) runs before
// authentication and only tracks the body; at most maxPendingBodyBytes, the
// request logger's preview, may be read before the gate classifies it
// (assignRequestBody). A request whose bearer is rejected or cannot be
// verified is never assigned: its pending bytes are released when it ends.
// Callers without a valid key therefore cannot spend the verified budget.

// Entry labels of the in-flight budget metrics (httpbody.Inflight).
const (
	inflightEntryVerified  = "mcp"
	inflightEntryAnonymous = "mcp_anonymous"
)

// maxAuthorizationHeaderBytes bounds a raw Authorization header (or the
// query key) before the bearer gate splits it: the authenticator's own bound.
const maxAuthorizationHeaderBytes = mcpauth.MaxAuthorizationHeaderLength

// maxPendingBodyBytes bounds the body bytes a request may read before the
// credential gate assigns it to a budget. Only the request logger's preview
// (httpLogBodyLimit + 1 bytes) reads that early; the bound leaves headroom
// above it while keeping unclassified memory per connection small.
const maxPendingBodyBytes int64 = 64 << 10

// verifiedTenantShareDivisor sets one API key's share of the verified budget.
const verifiedTenantShareDivisor = 4

// ingressBodyBudgets holds the transport's two in-flight budgets.
type ingressBodyBudgets struct {
	verified  *httpbody.Inflight
	anonymous *httpbody.Inflight
}

// ingressBodyBudgetsKey stores the budgets on a request's context, where the
// credential gate finds them.
type ingressBodyBudgetsKey struct{}

// newIngressBodyBudgets builds both budgets from a normalized transport budget.
func newIngressBodyBudgets(budget RequestBodyBudget) *ingressBodyBudgets {
	share := max(budget.MaxBytes, budget.MaxInflightBytes/verifiedTenantShareDivisor)
	return &ingressBodyBudgets{
		verified:  httpbody.NewInflight(inflightEntryVerified, budget.MaxInflightBytes, budget.MaxBytes, share),
		anonymous: httpbody.NewInflight(inflightEntryAnonymous, budget.AnonymousMaxInflightBytes, budget.AnonymousMaxBytes, 0),
	}
}

// attach stores the budgets on r's context.
func (b *ingressBodyBudgets) attach(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ingressBodyBudgetsKey{}, b))
}

// assignRequestBody classifies r's body for the in-flight budgets: verified
// requests are charged to the verified budget under tenant (the key's
// APIKeyHash), the others to the anonymous budget. It returns an error
// matching httpbody.IsInflightExhausted when the bytes read so far do not
// fit; the caller answers 503 (writeTransportBusy). A request served outside
// withRequestBodyBudget has nothing to assign.
func assignRequestBody(r *http.Request, verified bool, tenant string) error {
	budgets, ok := r.Context().Value(ingressBodyBudgetsKey{}).(*ingressBodyBudgets)
	if !ok || budgets == nil {
		return nil
	}
	charge, tracked := httpbody.ChargeFromContext(r.Context())
	if !tracked {
		return nil
	}
	budget := budgets.anonymous
	if verified {
		budget = budgets.verified
	}
	return charge.Assign(budget, tenant) //nolint:wrapcheck // the typed budget error is the contract
}

// logInflightRefusal logs a classification refused by a spent budget.
func logInflightRefusal(logger logSDK.Logger, class string, err error) {
	if logger != nil {
		logger.Debug("mcp request refused: in-flight body budget spent", zap.String("caller_class", class), zap.Error(err))
	}
}

// saturatingMultiple returns value × factor, saturating at math.MaxInt64.
func saturatingMultiple(value, factor int64) int64 {
	if value > 0 && factor > 0 && value > math.MaxInt64/factor {
		return math.MaxInt64
	}
	return value * factor
}
