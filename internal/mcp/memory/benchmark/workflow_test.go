package benchmark

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBenchmarkWorkflowsRecordCheckedOutCommit(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "..", "..")
	for _, name := range []string{"memory-benchmark.yml", "memory-benchmark-capture.yml", "memory-benchmark-results.yml"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", name))
			require.NoError(t, err)
			require.Contains(t, string(raw), "git rev-parse HEAD")
		})
	}
}

func TestLocalBenchmarkCommandsUseValidatedThreshold(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "..", "..")
	for _, path := range []string{
		"Makefile",
		filepath.Join(".github", "workflows", "memory-benchmark.yml"),
		filepath.Join(".github", "workflows", "memory-benchmark-capture.yml"),
		filepath.Join(".github", "workflows", "memory-benchmark-results.yml"),
	} {
		raw, err := os.ReadFile(filepath.Join(root, path))
		require.NoError(t, err, path)
		require.Contains(t, string(raw), "--min-score=0.20", path)
	}
}
