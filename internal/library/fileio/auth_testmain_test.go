package fileio

import (
	"os"
	"testing"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
)

// TestMain installs an explicit accept-all API key authenticator: these tests
// use synthetic bearers and must not depend on one-api. Tests that exercise
// rejection inject their own authenticator.
func TestMain(m *testing.M) {
	authtest.InstallAcceptAll()
	os.Exit(m.Run())
}
