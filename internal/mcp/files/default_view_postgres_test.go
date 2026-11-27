package files

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

// newPostgresViewService returns a service on a private PostgreSQL schema that
// is dropped afterwards. Use a disposable database.
func newPostgresViewService(t *testing.T) *Service {
	t.Helper()
	dsn := os.Getenv("FILEIO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("PostgreSQL behavior test: set FILEIO_TEST_POSTGRES_DSN; SQLite is not a substitute")
	}
	ctx := context.Background()
	cfg, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { require.NoError(t, admin.Close()) })
	_, err = admin.ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public")
	require.NoError(t, err)
	var nonce [12]byte
	_, err = rand.Read(nonce[:])
	require.NoError(t, err)
	schema := "fileio_view_" + hex.EncodeToString(nonce[:])
	_, err = admin.ExecContext(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, dropErr := admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		require.NoError(t, dropErr)
	})
	clientCfg := cfg.Copy()
	clientCfg.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*clientCfg)
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	settings := LoadSettingsFromConfig()
	settings.LockTimeout = 10 * time.Second
	settings.Search.Enabled = false
	svc, err := NewService(db, settings, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	return svc
}

// TestDefaultViewPostgresStatAndListUseOneSnapshot interleaves a committed
// change between the statements of a stat and a list. A default directory turns
// into a project file at the same name; the result must be the directory (the
// view when the snapshot began), never "not found", which no instant showed.
func TestDefaultViewPostgresStatAndListUseOneSnapshot(t *testing.T) {
	svc := newPostgresViewService(t)
	ctx := context.Background()
	auth := AuthContext{APIKeyHash: "tenant-a", APIKey: "key-a", UserIdentity: "user:a"}
	_, err := svc.Write(ctx, auth, DefaultProject, "/dir/a.md", "default", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	hideWithProjectFile := func(project string) func() {
		return func() {
			svc.mustWritePhysical(t, auth, project, "/dir", "a project file now hides the default subtree")
		}
	}
	var stat StatResult
	require.NoError(t, svc.withReadSnapshot(ctx, true, func(q sqlQueryer) error {
		interleaved := &interleavingQueryer{sqlQueryer: q, interleave: hideWithProjectFile("p")}
		var statErr error
		stat, statErr = svc.statQ(ctx, interleaved, auth.APIKeyHash, "p", "/dir", true)
		require.Greater(t, interleaved.statements, 1, "precondition: the stat needs several statements")
		return statErr
	}))
	require.True(t, stat.Exists)
	require.Equal(t, FileTypeDirectory, stat.Type)
	after, err := svc.Stat(ctx, auth, "p", "/dir")
	require.NoError(t, err)
	require.Equal(t, FileTypeFile, after.Type, "a new snapshot sees the committed change")

	var entry FileEntry
	var exists bool
	require.NoError(t, svc.withReadSnapshot(ctx, true, func(q sqlQueryer) error {
		interleaved := &interleavingQueryer{sqlQueryer: q, interleave: hideWithProjectFile("q")}
		var listErr error
		entry, exists, listErr = svc.listSelfEntry(ctx, interleaved, auth.APIKeyHash, "q", "/dir", true)
		return listErr
	}))
	require.True(t, exists)
	require.Equal(t, FileTypeDirectory, entry.Type)

	// A read is one statement on PostgreSQL as well.
	q := &interleavingQueryer{sqlQueryer: svc.db}
	file, err := svc.resolveFile(ctx, q, auth.APIKeyHash, "r", "/dir/a.md", true, true)
	require.NoError(t, err)
	require.Equal(t, DefaultProject, file.Project)
	require.Equal(t, 1, q.statements)
}
