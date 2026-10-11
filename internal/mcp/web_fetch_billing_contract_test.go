package mcp

import (
	"context"
	"testing"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/calllog"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

func TestWebFetchBillingAuditAfterFailure(t *testing.T) {
	for _, tc := range []struct {
		name, url      string
		billingErr     error
		outcome        oneapi.BillingOutcome
		bills, fetches int
		charged        bool
	}{
		{name: "accepted provider failure", url: "https://1.1.1.1", outcome: oneapi.BillingAccepted, bills: 1, fetches: 1, charged: true},
		{name: "denied before provider", url: "https://1.1.1.1", billingErr: &oneapi.BillingError{Outcome: oneapi.BillingDenied}, outcome: oneapi.BillingDenied, bills: 1},
		{name: "invalid URL before billing", url: "http://127.0.0.1/private", outcome: oneapi.BillingNotAttempted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &behaviorRecorder{}
			var bills, fetches int
			tool, err := tools.NewWebFetchTool(&rlibs.DB{}, log.Logger, func(context.Context) string { return "synthetic-token" },
				trackBillingOutcome(func(context.Context, string, oneapi.Price, string) error { bills++; return tc.billingErr }),
				func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) {
					fetches++
					return nil, errors.New("private-error-canary")
				})
			require.NoError(t, err)
			server := &Server{webFetch: tool, callLogger: recorder, logger: log.Logger}
			result, err := server.handleWebFetch(context.Background(), makeReq(map[string]any{"url": tc.url}))
			require.NoError(t, err)
			require.True(t, result.IsError)
			require.Equal(t, tc.bills, bills)
			require.Equal(t, tc.fetches, fetches)
			require.Len(t, recorder.records, 1)
			record := recorder.last()
			require.Equal(t, calllog.StatusError, record.Status)
			metadata := billingMetadata(t, record)
			require.Equal(t, string(tc.outcome), metadata.Outcome)
			require.Equal(t, tc.charged, metadata.Charged)
			expectedCost := 0
			if tc.charged {
				expectedCost = oneapi.PriceWebFetch.Int()
			}
			require.Equal(t, expectedCost, record.Cost)
			require.NotContains(t, getText(t, result), "private-error-canary")
		})
	}
}
