package auth

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/clientip"
)

// capacityAuthority accepts the simulated active keys and rejects the rest.
type capacityAuthority struct{}

// ValidateAPIKey accepts keys with the "sk-active-" prefix.
func (capacityAuthority) ValidateAPIKey(_ context.Context, apiKey string) error {
	if strings.HasPrefix(apiKey, "sk-active-") {
		return nil
	}
	return ErrCredentialRejected
}

// capacityRun simulates active keys, each on its own client source, calling
// every 30 s on a fake clock with the default budgets. floodPerSecond random
// bearers from fresh sources are sent first in every simulated second once
// the flood starts. It returns how many active-key calls were refused as
// unavailable from measureFrom on.
func capacityRun(t *testing.T, activeKeys int, stagger time.Duration, floodPerSecond int, floodFrom, measureFrom, total time.Duration) int {
	t.Helper()
	authenticator, clock := newScriptedAuthenticatorFor(t, capacityAuthority{}, AuthenticatorOptions{})
	start := clock.Now()
	refused, flood := 0, 0
	for elapsed := time.Duration(0); elapsed < total; elapsed += time.Second {
		now := start.Add(elapsed)
		clock.Advance(now.Sub(clock.Now()))
		if elapsed >= floodFrom {
			for range floodPerSecond {
				flood++
				ctx := clientip.WithIP(context.Background(), fmt.Sprintf("10.%d.%d.%d", flood>>16&255, flood>>8&255, flood&255))
				_, _ = authenticator.Authenticate(ctx, fmt.Sprintf("Bearer sk-flood-%d", flood))
			}
		}
		for key := range activeKeys {
			first := time.Duration(key) * stagger
			if elapsed < first || (elapsed-first)%(30*time.Second) != 0 {
				continue
			}
			ctx := clientip.WithIP(context.Background(), fmt.Sprintf("198.18.%d.%d", key>>8, key&255))
			_, err := authenticator.Authenticate(ctx, fmt.Sprintf("Bearer sk-active-%d", key))
			if err != nil {
				require.True(t, IsUnavailable(err), "an issued key is never rejected: %v", err)
				if elapsed >= measureFrom {
					refused++
				}
			}
		}
	}
	return refused
}

// TestDefaultBudgetsCapacityForActiveKeys documents what the default budgets
// sustain (docs/manual/mcp.md, "Capacity"): every active key revalidates once
// per positive_ttl, drawing on the 0.2/s reserve and then the 1/s global
// budget, about 1.2 calls/s or 360 keys per 5 minutes. Under a flood that
// spends the global budget only the reserve remains, about 60 keys; beyond
// that, revalidations are refused as unavailable (never as rejected), since a
// local refusal never grants stale grace.
func TestDefaultBudgetsCapacityForActiveKeys(t *testing.T) {
	const hour = time.Hour
	t.Run("300 active keys without a flood", func(t *testing.T) {
		require.Zero(t, capacityRun(t, 300, 2*time.Second, 0, hour, 15*time.Minute, hour))
	})
	t.Run("50 known keys under a flood", func(t *testing.T) {
		require.Zero(t, capacityRun(t, 50, 6*time.Second, 5, 5*time.Minute, 5*time.Minute, hour))
	})
	t.Run("150 known keys under a flood exceed the reserve", func(t *testing.T) {
		require.Positive(t, capacityRun(t, 150, 2*time.Second, 5, 5*time.Minute, 15*time.Minute, hour),
			"the reserve is finite; raise revalidation_rate_per_second or positive_ttl for more keys")
	})
}

// TestValidationCapacityMatchesTheManual keeps the startup capacity report
// equal to the figures the manual derives from the defaults, and covers a
// disabled reserve.
func TestValidationCapacityMatchesTheManual(t *testing.T) {
	require.Equal(t, ValidationCapacity{ActiveKeys: 360, ActiveKeysUnderFlood: 60, MaxCallsPerWindow: 266},
		DefaultValidationSettings().Capacity())

	noReserve := DefaultValidationSettings()
	noReserve.RevalidationRatePerSecond = 0
	require.Equal(t, ValidationCapacity{ActiveKeys: 300, ActiveKeysUnderFlood: 0, MaxCallsPerWindow: 210}, noReserve.Capacity())
}
