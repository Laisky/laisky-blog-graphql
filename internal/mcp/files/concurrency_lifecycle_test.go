package files_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestFileIOPostgresLifecycle checks all legal outcomes of two concurrent
// mutations. It deliberately does not equate serial execution with safe editing.
func TestFileIOPostgresLifecycle(t *testing.T) {
	t.Run("delete_vs_write", func(t *testing.T) {
		f := newRaceFixture(t, "postgres", nil)
		f.write(t, 0, "/state.txt", "base", files.WriteModeTruncate, 0)
		for _, err := range parallelRaceCalls(t, f.ctx, 2, func(i int) error {
			if i == 0 {
				_, err := f.svc[i].Delete(f.ctx, f.auth, "race", "/state.txt", false)
				return err
			}
			_, err := f.svc[i].Write(f.ctx, f.auth, "race", "/state.txt", "edited", "utf-8", 0, files.WriteModeTruncate)
			return err
		}) {
			require.NoError(t, err)
		}
		stat, err := f.svc[0].Stat(f.ctx, f.auth, "race", "/state.txt")
		require.NoError(t, err)
		if stat.Exists { // Delete committed before the create-on-write.
			f.assertStored(t, "/state.txt", "edited")
			require.Equal(t, []string{"base"}, f.history(t, "/state.txt"))
		} else { // Write committed before delete.
			require.Equal(t, []string{"base", "edited"}, f.history(t, "/state.txt"))
			_, err = f.svc[0].Read(f.ctx, f.auth, "race", "/state.txt", 0, -1)
			require.True(t, files.IsCode(err, files.ErrCodeNotFound))
		}
		require.Equal(t, 3, f.count(t, "mcp_file_index_jobs"))
	})

	t.Run("rename_vs_write", func(t *testing.T) {
		f := newRaceFixture(t, "postgres", nil)
		f.write(t, 0, "/old.txt", "base", files.WriteModeTruncate, 0)
		for _, err := range parallelRaceCalls(t, f.ctx, 2, func(i int) error {
			if i == 0 {
				_, err := f.svc[i].Rename(f.ctx, f.auth, "race", "/old.txt", "/new.txt", false)
				return err
			}
			_, err := f.svc[i].Write(f.ctx, f.auth, "race", "/old.txt", "edited", "utf-8", 0, files.WriteModeTruncate)
			return err
		}) {
			require.NoError(t, err)
		}
		old, err := f.svc[0].Stat(f.ctx, f.auth, "race", "/old.txt")
		require.NoError(t, err)
		if old.Exists { // Rename first, then a new file at the old path.
			f.assertStored(t, "/old.txt", "edited")
			f.assertStored(t, "/new.txt", "base")
			require.Empty(t, f.history(t, "/new.txt"))
		} else {
			f.assertStored(t, "/new.txt", "edited")
			require.Equal(t, []string{"base"}, f.history(t, "/new.txt"))
		}
		require.Empty(t, f.history(t, "/old.txt"))
		require.Equal(t, 4, f.count(t, "mcp_file_index_jobs"))
	})

	t.Run("restore_vs_write", func(t *testing.T) {
		f := newRaceFixture(t, "postgres", nil)
		f.write(t, 0, "/restore.txt", "A", files.WriteModeTruncate, 0)
		f.write(t, 0, "/restore.txt", "B", files.WriteModeTruncate, 0)
		versions, err := f.svc[0].ListVersions(f.ctx, f.auth, "race", "/restore.txt")
		require.NoError(t, err)
		require.Len(t, versions, 1)
		for _, err := range parallelRaceCalls(t, f.ctx, 2, func(i int) error {
			if i == 0 {
				_, err := f.svc[i].RestoreVersion(f.ctx, f.auth, "race", "/restore.txt", versions[0].ID)
				return err
			}
			_, err := f.svc[i].Write(f.ctx, f.auth, "race", "/restore.txt", "C", "utf-8", 0, files.WriteModeTruncate)
			return err
		}) {
			require.NoError(t, err)
		}
		states := append(f.history(t, "/restore.txt"), f.read(t, 1, "/restore.txt"))
		require.Contains(t, [][]string{{"A", "B", "A", "C"}, {"A", "B", "C", "A"}}, states)
		f.assertLedger(t, "/restore.txt", states[:3], states)
		f.assertStored(t, "/restore.txt", states[3])
	})
}

// TestFileIOPostgresScopeIsolation proves unrelated projects and tenants make
// progress while a real project lock is held, without sharing content.
func TestFileIOPostgresScopeIsolation(t *testing.T) {
	f := newRaceFixture(t, "postgres", nil)
	f.write(t, 0, "/same.txt", "base", files.WriteModeTruncate, 0)
	gate := &commitGate{prepared: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(gate.Release)
	writer := f.service(t, 0, gate)
	done := make(chan error, 1)
	go func() {
		_, err := writer.Write(f.ctx, f.auth, "race", "/same.txt", "held", "utf-8", 0, files.WriteModeTruncate)
		done <- err
	}()
	select {
	case <-gate.prepared:
	case <-f.ctx.Done():
		t.Fatal("writer never reached gate")
	}
	other := files.AuthContext{APIKeyHash: strings.Repeat("b", 64)}
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	for _, scope := range []struct {
		auth    files.AuthContext
		project string
		content string
	}{{f.auth, "other", "other-project"}, {other, "race", "other-tenant"}} {
		_, err := f.svc[1].Write(ctx, scope.auth, scope.project, "/same.txt", scope.content, "utf-8", 0, files.WriteModeTruncate)
		require.NoError(t, err, "unrelated scope must finish before held transaction releases")
		read, err := f.svc[1].Read(ctx, scope.auth, scope.project, "/same.txt", 0, -1)
		require.NoError(t, err)
		require.Equal(t, scope.content, read.Content)
	}
	f.assertStored(t, "/same.txt", "base")
	require.Equal(t, 1, f.count(t, "mcp_file_index_jobs"))
	gate.Release()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-f.ctx.Done():
		t.Fatal("writer failed to finish")
	}
	f.assertStored(t, "/same.txt", "held")
}

// TestFileIORejectedWrites checks that failed validations leave no content,
// snapshot, or indexing side effects and that the next writer can proceed.
func TestFileIORejectedWrites(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			f := newRaceFixture(t, backend, func(s *files.Settings) { s.MaxProjectBytes = 6 })
			f.write(t, 0, "/state.txt", "base", files.WriteModeTruncate, 0)
			for i, tc := range []struct {
				mode    files.WriteMode
				content string
				offset  int64
				code    files.ErrorCode
			}{{files.WriteModeOverwrite, "X", 99, files.ErrCodeInvalidOffset},
				{files.WriteModeAppend, "overflow", 0, files.ErrCodeQuotaExceeded}} {
				t.Run(fmt.Sprintf("failure-%d", i), func(t *testing.T) {
					_, err := f.svc[1].Write(f.ctx, f.auth, "race", "/state.txt", tc.content, "utf-8", tc.offset, tc.mode)
					require.True(t, files.IsCode(err, tc.code), "%v", err)
					f.assertStored(t, "/state.txt", "base")
					require.Equal(t, 0, f.count(t, "mcp_file_versions"))
					require.Equal(t, 1, f.count(t, "mcp_file_index_jobs"))
				})
			}
			f.write(t, 0, "/state.txt", "next", files.WriteModeTruncate, 0)
			f.assertLedger(t, "/state.txt", []string{"base"}, []string{"base", "next"})
		})
	}
}
