package controller

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/service"
)

// TestAccountFoldNeverSplitsAStoreAccount checks the spelling-key contract
// against the MongoDB lookup (service.NormalizeLoginAccount): inputs the store
// resolves to one account must share one spelling key, or an attacker could
// spread guesses over spellings to multiply the account budget. The fold may
// group more (see TestAuthSuccessKeepsCollidingIdentityEnforced for why that
// is safe), never fewer.
func TestAccountFoldNeverSplitsAStoreAccount(t *testing.T) {
	tracker := newAuthChallengeTracker(0, 0, 0, 0, 0)
	bases := []string{
		"alice@example.test", "ALICE@EXAMPLE.TEST", " Alice@Example.Test\t",
		"KELVIN@example.test", "Kelvin@example.test", // KELVIN SIGN lower-cases to k
		"İstanbul@example.test", "i̇stanbul@example.test", // dotted capital I and its lower-case form
		"STRASSE@example.test", "Ångström@example.test", "ångström@example.test",
		"Σσς@example.test", "σσσ@example.test", // Greek sigma forms
		"Ａlice@example.test", "ａlice@example.test", // full-width A/a
		"user name@example.test", "user name@example.test",
	}
	for _, a := range bases {
		for _, b := range bases {
			if service.NormalizeLoginAccount(a) != service.NormalizeLoginAccount(b) {
				continue
			}
			require.Equal(t, tracker.accountKey(a), tracker.accountKey(b),
				"%q and %q are one MongoDB account but fold apart", a, b)
		}
	}
	// Sanity: the corpus exercises store-equal pairs beyond identical strings.
	require.Equal(t, service.NormalizeLoginAccount("Kelvin@example.test"), strings.ToLower("KELVIN@example.test"))
}
