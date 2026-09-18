package mcp

import (
	"context"
	"testing"
	"time"

	goerrors "github.com/Laisky/errors/v2"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/calllog"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// billingMetadata pulls the recorded billing classification off an audit row.
func billingMetadata(t *testing.T, input calllog.RecordInput) *calllog.BillingMetadata {
	t.Helper()
	require.NotNil(t, input.Billing, "every audited invocation must carry a billing classification")
	return input.Billing
}

// TestRecordToolInvocationFollowsBillingOutcome is the G06 reconciliation.
//
// Before this contract the MCP wrapper derived the recorded cost from the TOOL
// result: a provider failure recorded zero even when the consume had already
// been accepted and the user's quota really was spent. The audit row therefore
// understated usage and could not be used to reconcile against the billing
// service. The cost must follow the billing outcome instead, and the row must
// say which outcome it was.
func TestRecordToolInvocationFollowsBillingOutcome(t *testing.T) {
	t.Parallel()

	t.Run("an accepted consume stays charged when the provider then fails", func(t *testing.T) {
		t.Parallel()
		recorder := &behaviorRecorder{}
		s := &Server{callLogger: recorder, logger: log.Logger}
		ctx := withBillingAttemptTracking(context.Background())
		markBillingOutcome(ctx, oneapi.BillingAccepted)

		s.recordToolInvocation(ctx, "web_search", "sk-test", nil, time.Now(), time.Second, 42,
			nil, goerrors.New("search backend down"))

		require.Len(t, recorder.records, 1)
		require.Equal(t, calllog.StatusError, recorder.last().Status,
			"the operation still failed for the caller")
		require.Equal(t, 42, recorder.last().Cost,
			"an accepted consume spent the quota; recording zero hides a real charge")
		metadata := billingMetadata(t, recorder.last())
		require.Equal(t, string(oneapi.BillingAccepted), metadata.Outcome)
		require.True(t, metadata.Charged)
		require.False(t, metadata.Indeterminate)
	})

	t.Run("a denied consume records no charge", func(t *testing.T) {
		t.Parallel()
		recorder := &behaviorRecorder{}
		s := &Server{callLogger: recorder, logger: log.Logger}
		ctx := withBillingAttemptTracking(context.Background())
		markBillingOutcome(ctx, oneapi.BillingDenied)

		s.recordToolInvocation(ctx, "web_search", "sk-test", nil, time.Now(), time.Second, 42,
			mcpgo.NewToolResultError("billing check failed"), nil)

		require.Equal(t, 0, recorder.last().Cost)
		metadata := billingMetadata(t, recorder.last())
		require.Equal(t, string(oneapi.BillingDenied), metadata.Outcome)
		require.False(t, metadata.Charged)
		require.False(t, metadata.Indeterminate)
	})

	t.Run("an undetermined consume is charged and flagged, never free", func(t *testing.T) {
		t.Parallel()
		recorder := &behaviorRecorder{}
		s := &Server{callLogger: recorder, logger: log.Logger}
		ctx := withBillingAttemptTracking(context.Background())
		markBillingOutcome(ctx, oneapi.BillingUnknown)

		s.recordToolInvocation(ctx, "web_search", "sk-test", nil, time.Now(), time.Second, 42,
			mcpgo.NewToolResultError("billing check failed"), nil)

		require.Equal(t, 42, recorder.last().Cost,
			"an unresolved consume may already have spent the quota")
		metadata := billingMetadata(t, recorder.last())
		require.Equal(t, string(oneapi.BillingUnknown), metadata.Outcome)
		require.True(t, metadata.Charged)
		require.True(t, metadata.Indeterminate, "the row is not a receipt and needs reconciliation")
	})

	t.Run("a free tool that never billed records not_attempted", func(t *testing.T) {
		t.Parallel()
		recorder := &behaviorRecorder{}
		s := &Server{callLogger: recorder, logger: log.Logger}
		ctx := withBillingAttemptTracking(context.Background())

		s.recordToolInvocation(ctx, "file_read", "sk-test", nil, time.Now(), time.Second, 0,
			mcpgo.NewToolResultText("ok"), nil)

		require.Equal(t, 0, recorder.last().Cost)
		metadata := billingMetadata(t, recorder.last())
		require.Equal(t, string(oneapi.BillingNotAttempted), metadata.Outcome)
		require.False(t, metadata.Charged)
		require.Equal(t, 0, metadata.Price)
	})

	t.Run("a paid tool that exited before billing records not_attempted", func(t *testing.T) {
		t.Parallel()
		recorder := &behaviorRecorder{}
		s := &Server{callLogger: recorder, logger: log.Logger}
		ctx := withBillingAttemptTracking(context.Background())

		// Validation rejected the request before the consume call happened.
		s.recordToolInvocation(ctx, "web_search", "sk-test", nil, time.Now(), time.Second, 42,
			mcpgo.NewToolResultError("query cannot be empty"), nil)

		require.Equal(t, 0, recorder.last().Cost)
		metadata := billingMetadata(t, recorder.last())
		require.Equal(t, string(oneapi.BillingNotAttempted), metadata.Outcome)
		require.Equal(t, 42, metadata.Price,
			"the configured price documents what the operation would have cost")
		require.False(t, metadata.Charged)
	})
}

// TestTrackedBillingReporterClassifiesOutcome keeps the classification attached
// at the single place every paid tool shares, so no tool can forget it.
func TestTrackedBillingReporterClassifiesOutcome(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		reported error
		expected oneapi.BillingOutcome
	}{
		{name: "accepted", reported: nil, expected: oneapi.BillingAccepted},
		{name: "denied", reported: &oneapi.BillingError{Outcome: oneapi.BillingDenied}, expected: oneapi.BillingDenied},
		{name: "unknown", reported: &oneapi.BillingError{Outcome: oneapi.BillingUnknown}, expected: oneapi.BillingUnknown},
		{name: "unclassified defaults to unknown", reported: goerrors.New("boom"), expected: oneapi.BillingUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reporter := trackBillingOutcome(func(context.Context, string, oneapi.Price, string) error {
				return tc.reported
			})
			ctx := withBillingAttemptTracking(context.Background())
			err := reporter(ctx, "sk-test", oneapi.PriceWebSearch, "web_search")
			if tc.reported == nil {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.True(t, billingAttemptRecorded(ctx), "the attempt must be marked either way")
			outcome, ok := billingOutcomeFromContext(ctx)
			require.True(t, ok)
			require.Equal(t, tc.expected, outcome)
		})
	}
}
