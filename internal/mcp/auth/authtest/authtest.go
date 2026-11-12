// Package authtest provides explicit test doubles for MCP API key validation.
// Production code must never import it.
package authtest

import (
	"context"
	"strings"
	"sync/atomic"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
)

// NewAcceptAll returns an Authenticator that accepts every syntactically valid
// key, standing in for one-api in tests that use synthetic bearers.
func NewAcceptAll() *mcpauth.Authenticator {
	authenticator, err := mcpauth.NewAuthenticator(mcpauth.InsecureAcceptAllValidator{}, mcpauth.UnmeteredOptions("test-accept-all"))
	if err != nil {
		panic(err)
	}
	return authenticator
}

// InstallAcceptAll installs an accept-all Authenticator process-wide. Call it
// from TestMain of packages whose tests use synthetic bearers.
func InstallAcceptAll() {
	mcpauth.Install(NewAcceptAll())
}

// Verified returns the verified auth context a test bearer would receive.
// apiKey may carry a "Bearer " prefix.
func Verified(apiKey string) *mcpauth.Context {
	header := apiKey
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(header)), "bearer ") {
		header = "Bearer " + apiKey
	}
	authenticated, err := NewAcceptAll().Authenticate(context.Background(), header)
	if err != nil {
		panic(err)
	}
	return authenticated
}

// WithVerified stores the verified auth context for apiKey on ctx.
func WithVerified(ctx context.Context, apiKey string) context.Context {
	return mcpauth.WithContext(ctx, Verified(apiKey))
}

// Cleanuper is the subset of testing.TB that Swap needs.
type Cleanuper interface {
	Cleanup(func())
}

// Swap installs authenticator process-wide for one test and restores the
// previous one afterwards. Only sequential tests may use it: Go never runs a
// sequential test concurrently with parallel ones.
func Swap(t Cleanuper, authenticator *mcpauth.Authenticator) {
	previous := mcpauth.Installed()
	mcpauth.Install(authenticator)
	t.Cleanup(func() { mcpauth.Install(previous) })
}

// NewIssuedOnly returns an Authenticator whose authority accepts only the
// listed keys and rejects every other key, counting authority calls in calls.
func NewIssuedOnly(calls *atomic.Int64, issued ...string) *mcpauth.Authenticator {
	allowed := make(map[string]bool, len(issued))
	for _, key := range issued {
		allowed[key] = true
	}
	validator := mcpauth.ValidatorFunc(func(_ context.Context, apiKey string) error {
		if calls != nil {
			calls.Add(1)
		}
		if allowed[apiKey] {
			return nil
		}
		return mcpauth.ErrCredentialRejected
	})
	authenticator, err := mcpauth.NewAuthenticator(validator, mcpauth.AuthenticatorOptions{Name: "test-issued-only"})
	if err != nil {
		panic(err)
	}
	return authenticator
}
