package search

import (
	"context"
	"testing"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

// TestBillingMetadataForMatchesTheOutcomeContract pins the shared classifier
// both interfaces use, so a GraphQL row and an MCP row describe the same
// situation with the same outcome.
func TestBillingMetadataForMatchesTheOutcomeContract(t *testing.T) {
	t.Parallel()

	t.Run("no error is an accepted, charged consume", func(t *testing.T) {
		t.Parallel()
		metadata := BillingMetadataFor(oneapi.PriceWebSearch, nil)
		require.Equal(t, string(oneapi.BillingAccepted), metadata.Outcome)
		require.True(t, metadata.Charged)
		require.False(t, metadata.Indeterminate)
		require.Equal(t, oneapi.PriceWebSearch.Int(), metadata.Price)
	})

	t.Run("a provider failure after an accepted consume stays charged", func(t *testing.T) {
		t.Parallel()
		// The provider error is not a billing error, so the consume that
		// already succeeded must still be recorded as spent.
		metadata := BillingMetadataFor(oneapi.PriceWebSearch, errors.New("search backend down"))
		require.Equal(t, string(oneapi.BillingAccepted), metadata.Outcome)
		require.True(t, metadata.Charged)
	})

	t.Run("a denied consume is not charged", func(t *testing.T) {
		t.Parallel()
		metadata := BillingMetadataFor(oneapi.PriceWebSearch,
			errors.Wrap(&oneapi.BillingError{Outcome: oneapi.BillingDenied, Status: 402}, "check user external billing"))
		require.Equal(t, string(oneapi.BillingDenied), metadata.Outcome)
		require.False(t, metadata.Charged)
	})

	t.Run("an undetermined consume is charged and flagged", func(t *testing.T) {
		t.Parallel()
		metadata := BillingMetadataFor(oneapi.PriceWebSearch,
			errors.Wrap(&oneapi.BillingError{Outcome: oneapi.BillingUnknown}, "do request"))
		require.Equal(t, string(oneapi.BillingUnknown), metadata.Outcome)
		require.True(t, metadata.Charged)
		require.True(t, metadata.Indeterminate)
	})
}

// TestDeniedBillingIsAudited closes the gap the matrix named: a GraphQL request
// that billing refused used to return before the resolver wrote anything, so a
// denial left no trace at all while MCP recorded one.
func TestDeniedBillingIsAudited(t *testing.T) {
	rdb := &rlibs.DB{}
	resolver := NewMutationResolver(nil, rdb, nil)
	resolver.billingChecker = func(context.Context, string, oneapi.Price, string) error {
		return errors.Wrap(&oneapi.BillingError{Outcome: oneapi.BillingDenied, Status: 402,
			Reason: "insufficient quota"}, "check user external billing")
	}
	fetched := 0
	resolver.fetcher = func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) {
		fetched++
		return []byte("body"), nil
	}

	captured := []map[string]any{}
	resolver.auditHook = func(parameters map[string]any) { captured = append(captured, parameters) }

	_, err := resolver.WebFetch(contractContext(t), "https://8.8.8.8/", nil)
	require.Error(t, err)
	require.Equal(t, oneapi.BillingDenied, oneapi.ClassifyBillingOutcome(err),
		"the caller must be able to tell a denial from an unresolved consume")
	require.Zero(t, fetched, "a denied consume must not reach the provider")
	require.Len(t, captured, 1, "a denial must still be audited")
	require.Equal(t, "https://8.8.8.8", captured[0]["url"])
}

// TestUnknownBillingIsAuditedAsIndeterminate keeps the most dangerous case
// honest: a timeout must not be presented to the caller as a denial, and must
// not be recorded as a free request.
func TestUnknownBillingIsAuditedAsIndeterminate(t *testing.T) {
	rdb := &rlibs.DB{}
	resolver := NewMutationResolver(nil, rdb, nil)
	resolver.billingChecker = func(context.Context, string, oneapi.Price, string) error {
		return errors.Wrap(&oneapi.BillingError{Outcome: oneapi.BillingUnknown,
			Reason: "consume request did not complete"}, "do request")
	}
	fetched := 0
	resolver.fetcher = func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) {
		fetched++
		return []byte("body"), nil
	}

	_, err := resolver.WebFetch(contractContext(t), "https://8.8.8.8/", nil)
	require.Error(t, err)
	outcome := oneapi.ClassifyBillingOutcome(err)
	require.Equal(t, oneapi.BillingUnknown, outcome)
	require.True(t, outcome.Charged(), "an unresolved consume may already have spent quota")
	require.True(t, outcome.Indeterminate())
	require.False(t, outcome.SafeToRetry(), "an unresolved consume must not be replayed automatically")
	require.Zero(t, fetched)

	metadata := BillingMetadataFor(oneapi.PriceWebFetch, err)
	require.Equal(t, oneapi.PriceWebFetch.Int(), metadata.Price)
	require.True(t, metadata.Charged)
	require.True(t, metadata.Indeterminate)
}
