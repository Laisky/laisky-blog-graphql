package mcp

import (
	"fmt"
	"os"
	"testing"
)

// TestMain isolates every legacy caller test from the application's default
// journal directory. Individual tests may select their own t.TempDir override.
func TestMain(m *testing.M) { os.Exit(runIsolatedCallerTests(m)) }

// runIsolatedCallerTests owns and removes only its newly created test directory.
func runIsolatedCallerTests(m *testing.M) (code int) {
	directory, err := os.MkdirTemp("", "graphql-caller-failure-test-")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "create caller journal test directory: %v\n", err)
		return 2
	}
	defer func() {
		if err := os.RemoveAll(directory); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "remove owned caller journal test directory: %v\n", err)
			if code == 0 {
				code = 2
			}
		}
	}()
	if err := os.Setenv("CRAWLER_FAILURE_LOG_DIR", directory); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "isolate caller journal tests: %v\n", err)
		return 2
	}
	return m.Run()
}
