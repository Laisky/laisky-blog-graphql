package files_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// raceFixture uses separate sql.DB pools and Service instances, not a process mutex.
// SQLite is only used for deterministic client histories. PostgreSQL is required
// for claims about simultaneous transactions, advisory locks, and MVCC visibility.
type raceFixture struct {
	ctx      context.Context
	db       [2]*sql.DB
	svc      [2]*files.Service
	auth     files.AuthContext
	settings files.Settings
	postgres bool
}

func newRaceFixture(t *testing.T, backend string, configure func(*files.Settings)) *raceFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	f := &raceFixture{ctx: ctx, postgres: backend == "postgres", auth: files.AuthContext{APIKeyHash: strings.Repeat("a", 64)}}
	f.settings = files.LoadSettingsFromConfig()
	f.settings.Search.Enabled = false // No real credentials, Redis, LLMs, or index workers.
	f.settings.Index.FileSummary.Enabled = false
	f.settings.Security.EncryptionKEKs = map[uint16]string{1: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}
	f.settings.MaxPayloadBytes = 1 << 20
	f.settings.MaxFileBytes = 2 << 20
	f.settings.MaxProjectBytes = 16 << 20
	f.settings.LockTimeout = 20 * time.Second
	if configure != nil {
		configure(&f.settings)
	}
	if f.postgres {
		f.openPostgres(t)
	} else {
		require.Equal(t, "sqlite", backend)
		dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "fileio.db")) + "?_busy_timeout=5000&_journal_mode=WAL"
		for i := range f.db {
			db, err := sql.Open("sqlite3", dsn)
			require.NoError(t, err)
			f.db[i] = db
			t.Cleanup(func() { require.NoError(t, db.Close()) })
		}
	}
	for i := range f.db {
		// More than one connection: a one-connection pool would hide locking bugs.
		f.db[i].SetMaxOpenConns(12)
		f.db[i].SetMaxIdleConns(12)
		require.NoError(t, f.db[i].PingContext(ctx))
		f.svc[i] = f.service(t, i, nil)
	}
	return f
}

func (f *raceFixture) openPostgres(t *testing.T) {
	t.Helper()
	dsn := os.Getenv("FILEIO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("PostgreSQL behavior test: set FILEIO_TEST_POSTGRES_DSN; SQLite is not a substitute")
	}
	cfg, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	admin := stdlib.OpenDB(*cfg)
	require.NoError(t, admin.PingContext(f.ctx))
	t.Cleanup(func() { require.NoError(t, admin.Close()) })
	// A fresh private schema protects unrelated data. Only this random schema is
	// dropped. Use a disposable database; vector must be installed in public.
	_, err = admin.ExecContext(f.ctx, "CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public")
	require.NoError(t, err)
	var nonce [12]byte
	_, err = rand.Read(nonce[:])
	require.NoError(t, err)
	schema := "fileio_race_" + hex.EncodeToString(nonce[:])
	_, err = admin.ExecContext(f.ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, dropErr := admin.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE")
		require.NoError(t, dropErr)
	})
	for i := range f.db {
		clientCfg := cfg.Copy()
		clientCfg.RuntimeParams["search_path"] = schema + ",public"
		clientCfg.RuntimeParams["default_transaction_isolation"] = "read committed"
		db := stdlib.OpenDB(*clientCfg)
		f.db[i] = db
		t.Cleanup(func() { require.NoError(t, db.Close()) })
	}
}

func (f *raceFixture) service(t *testing.T, client int, lock files.LockProvider) *files.Service {
	t.Helper()
	svc, err := files.NewService(f.db[client], f.settings, nil, nil, nil, nil, nil, lock, func() time.Time {
		// Deliberately equal timestamps: neither wall clock nor snapshot count is a CAS token.
		return time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	})
	require.NoError(t, err)
	return svc
}

func (f *raceFixture) query(query string) string {
	if !f.postgres {
		return query
	}
	var result strings.Builder
	n := 0
	for _, c := range query {
		if c == '?' {
			n++
			fmt.Fprintf(&result, "$%d", n)
		} else {
			result.WriteRune(c)
		}
	}
	return result.String()
}

func (f *raceFixture) write(t *testing.T, client int, path, content string, mode files.WriteMode, offset int64) {
	t.Helper()
	result, err := f.svc[client].Write(f.ctx, f.auth, "race", path, content, "utf-8", offset, mode)
	require.NoError(t, err)
	require.Equal(t, int64(len(content)), result.BytesWritten)
}

func (f *raceFixture) read(t *testing.T, client int, path string) string {
	t.Helper()
	result, err := f.svc[client].Read(f.ctx, f.auth, "race", path, 0, -1)
	require.NoError(t, err)
	return result.Content
}

// stored reads content, size, hash, and identity in ONE statement/snapshot.
// Comparing an independent Stat with Read during writes would itself be racy.
func (f *raceFixture) stored(path string) (uint64, string, error) {
	var id uint64
	var content []byte
	var size int64
	var hash string
	err := f.db[1].QueryRowContext(f.ctx, f.query(`SELECT id, content, size, content_hash FROM mcp_files
		WHERE apikey_hash = ? AND project = ? AND path = ? AND deleted = FALSE AND system_owner = ?`),
		f.auth.APIKeyHash, "race", path, "").Scan(&id, &content, &size, &hash)
	if err != nil {
		return 0, "", errors.Wrap(err, "read committed file state")
	}
	if size != int64(len(content)) || hash != files.HashFileContent(content) {
		return 0, "", errors.New("content/size/hash describe different generations")
	}
	return id, string(content), nil
}

func (f *raceFixture) assertStored(t *testing.T, path, expected string) {
	t.Helper()
	_, content, err := f.stored(path)
	require.NoError(t, err)
	require.Equal(t, expected, content)
}

func (f *raceFixture) count(t *testing.T, table string) int {
	t.Helper()
	require.Contains(t, []string{"mcp_files", "mcp_file_versions", "mcp_file_index_jobs"}, table)
	var n int
	err := f.db[1].QueryRowContext(f.ctx, f.query("SELECT COUNT(*) FROM "+table+" WHERE apikey_hash = ? AND project = ? AND system_owner = ?"),
		f.auth.APIKeyHash, "race", "").Scan(&n)
	require.NoError(t, err)
	return n
}

// parallelRaceCalls starts all workers behind one barrier. Errors are collected
// by the test goroutine; require.FailNow is never called from a worker.
func parallelRaceCalls(t *testing.T, ctx context.Context, n int, call func(int) error) []error {
	t.Helper()
	ready := make(chan struct{}, n)
	start := make(chan struct{})
	type outcome struct {
		index int
		err   error
	}
	done := make(chan outcome, n)
	for i := range n {
		go func() {
			ready <- struct{}{}
			select {
			case <-start:
				done <- outcome{i, call(i)}
			case <-ctx.Done():
				done <- outcome{i, ctx.Err()}
			}
		}()
	}
	for range n {
		<-ready
	}
	close(start)
	errs := make([]error, n)
	for range n {
		select {
		case result := <-done:
			errs[result.index] = result.err
		case <-ctx.Done():
			t.Fatal("concurrent calls exceeded their shared deadline")
		}
	}
	return errs
}

// commitGate pauses AFTER the actual mutation, snapshot and outbox writes, while
// retaining DefaultLockProvider's real advisory lock and transaction. It is not
// an in-memory substitute for the lock under test.
type commitGate struct {
	prepared chan struct{}
	release  chan struct{}
	abort    error
	once     sync.Once
}

// WithProjectLock delegates production locking and exposes the pre-commit boundary.
func (g *commitGate) WithProjectLock(ctx context.Context, db *sql.DB, pg bool, key, project string, timeout time.Duration, fn func(*sql.Tx) error) error {
	return (files.DefaultLockProvider{}).WithProjectLock(ctx, db, pg, key, project, timeout, func(tx *sql.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		close(g.prepared)
		select {
		case <-g.release:
			return g.abort
		case <-ctx.Done():
			return errors.WithStack(ctx.Err())
		}
	})
}

// Release unblocks the held transaction, including during failed-test cleanup.
func (g *commitGate) Release() {
	g.once.Do(func() { close(g.release) })
}
