package search

import (
	"context"
	"testing"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/library/toolpolicy"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// TestDefaultEgressRejectsMissingRequestChain uses a fresh real configuration,
// not an explicit true override that would conceal a permissive shipped default.
func TestDefaultEgressRejectsMissingRequestChain(t *testing.T) {
	original := gconfig.Shared
	t.Cleanup(func() { gconfig.Shared = original })
	policy := toolpolicy.EgressPolicy{Host: "1.1.1.1", Addresses: []string{"1.1.1.1"}, MaxRedirects: 1}
	logger := log.Logger.Named("egress_review")
	for _, tc := range []struct {
		name         string
		configured   *bool
		wantVerified bool
	}{
		{"unset secure default", nil, true},
		{"explicit verification", reviewEgressBoolPointer(true), true},
		{"explicit unsafe opt-out", reviewEgressBoolPointer(false), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gconfig.Shared = gconfig.New()
			if tc.configured != nil {
				gconfig.Shared.Set(configKeyRequireVerified, *tc.configured)
			}
			settings := LoadEgressSettings()
			if settings.RequireVerified != tc.wantVerified {
				t.Errorf("RequireVerified=%v, want %v", settings.RequireVerified, tc.wantVerified)
			}
			for _, chain := range [][]string{nil, {}} {
				err := verifyRenderedEgress(context.Background(), logger, settings, policy, chain)
				if tc.wantVerified && !errors.Is(err, toolpolicy.ErrEgressUnverified) {
					t.Errorf("missing chain must not release a successful result: %v", err)
				}
				if !tc.wantVerified && err != nil {
					t.Fatalf("explicit compatibility behavior changed: %v", err)
				}
			}
			if err := verifyRenderedEgress(context.Background(), logger, settings, policy, []string{"https://1.1.1.1/document"}); err != nil {
				t.Fatalf("valid literal-IP chain rejected: %v", err)
			}
			if err := verifyRenderedEgress(context.Background(), logger, settings, policy,
				[]string{"https://1.1.1.1/document", "http://127.0.0.1/private"}); err == nil {
				t.Error("unsafe reported chain must be rejected even with an explicit opt-out")
			}
		})
	}
}

// reviewEgressBoolPointer keeps the configuration table explicit about absence vs false.
func reviewEgressBoolPointer(value bool) *bool { return &value }
