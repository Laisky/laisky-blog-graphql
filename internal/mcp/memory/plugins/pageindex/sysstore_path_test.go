package pageindex

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

func TestSysStorePathsSatisfyProductionFileContract(t *testing.T) {
	t.Parallel()

	paths := []string{treePath("doc-1"), indexPath(), metaPath()}
	for _, systemPath := range paths {
		systemPath := systemPath
		t.Run(systemPath, func(t *testing.T) {
			t.Parallel()
			require.NoError(t, files.ValidatePath(systemPath))
			require.Contains(t, systemPath, "/pageindex/")
		})
	}
}
