package files_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestDefaultCreateNeverUsesStaleNamespace holds an actual uncommitted
// default create, reads the intermediate committed view, and retries the
// original project create-only token. SQLite runs in WAL on independent pools.
func TestDefaultCreateNeverUsesStaleNamespace(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			f := newRaceFixture(t, backend, nil)
			gate := &commitGate{prepared: make(chan struct{}), release: make(chan struct{})}
			t.Cleanup(gate.Release)
			writer := f.service(t, 0, gate)
			const path = "/serial.txt"
			opts := files.WriteOpts{CreateOnly: true}
			done := make(chan error, 1)
			go func() {
				_, err := writer.WriteWith(f.ctx, f.auth, files.DefaultProject, path, "default", "utf-8", 0, files.WriteModeTruncate, opts)
				done <- err
			}()
			select {
			case <-gate.prepared:
			case <-f.ctx.Done():
				t.Fatal("default write did not reach its commit barrier")
			}
			reader := f.svc[1]
			_, err := reader.Read(f.ctx, f.auth, "race", path, 0, -1)
			require.True(t, files.IsCode(err, files.ErrCodeNotFound), "read must remain available and not observe uncommitted data: %v", err)
			_, err = reader.WriteWith(f.ctx, f.auth, "race", path, "local", "utf-8", 0, files.WriteModeTruncate, opts)
			requireRetryableTenantBusy(t, err)
			require.Zero(t, f.count(t, "mcp_files"))
			require.Zero(t, f.count(t, "mcp_file_versions"))
			require.Zero(t, f.count(t, "mcp_file_index_jobs"))
			gate.Release()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-f.ctx.Done():
				t.Fatal("default write did not commit")
			}
			read, err := reader.Read(f.ctx, f.auth, "race", path, 0, -1)
			require.NoError(t, err)
			require.Equal(t, "default", read.Content)
			_, err = reader.WriteWith(f.ctx, f.auth, "race", path, "local", "utf-8", 0, files.WriteModeTruncate, opts)
			require.True(t, files.IsCode(err, files.ErrCodeVersionConflict), "%v", err)
			require.Zero(t, f.count(t, "mcp_files"))
		})
	}
}

func requireRetryableTenantBusy(t *testing.T, err error) {
	t.Helper()
	typed, ok := files.AsError(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, files.ErrCodeResourceBusy, typed.Code)
	require.True(t, typed.Retryable)
}
