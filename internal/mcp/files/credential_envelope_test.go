package files_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"sync"
	"testing"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// credentialTestBackends runs each credential envelope scenario on both
// storage backends; the PostgreSQL run is skipped without FILEIO_TEST_POSTGRES_DSN.
var credentialTestBackends = []string{"sqlite", "postgres"}

// credentialTTL is the envelope lifetime the scenarios configure.
const credentialTTL = time.Minute

// movableClock is a service clock the test advances.
type movableClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *movableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *movableClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// embedCall is one embedding request: the caller key it carried and its inputs.
type embedCall struct {
	apiKey string
	inputs []string
}

// recordingEmbedder records which key embedded which text.
type recordingEmbedder struct {
	mu    sync.Mutex
	calls []embedCall
	// failures is how many upcoming calls fail, as a model outage would.
	failures int
}

// EmbedTexts records the call and returns one vector of the production
// dimension per input, or an error while failures remain.
func (e *recordingEmbedder) EmbedTexts(_ context.Context, apiKey string, inputs []string) ([]pgvector.Vector, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, embedCall{apiKey: apiKey, inputs: append([]string(nil), inputs...)})
	if e.failures > 0 {
		e.failures--
		return nil, errors.New("embedding provider unavailable")
	}
	vectors := make([]pgvector.Vector, 0, len(inputs))
	for range inputs {
		vector := make([]float32, 1536)
		vector[0] = 1
		vectors = append(vectors, pgvector.NewVector(vector))
	}
	return vectors, nil
}

func (e *recordingEmbedder) Calls() []embedCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]embedCall(nil), e.calls...)
}

// errInjectedAbort fails a mutation after all of its writes, before commit.
var errInjectedAbort = errors.New("injected failure before commit")

// abortBeforeCommit runs each mutation under the production locks and
// transaction, then fails it after every write, as a failed commit would.
type abortBeforeCommit struct{}

// WithTenantLock delegates to the production lock and aborts before commit.
func (abortBeforeCommit) WithTenantLock(ctx context.Context, db *sql.DB, pg bool, key string, fn func(*sql.Tx) error) error {
	return (files.DefaultLockProvider{}).WithTenantLock(ctx, db, pg, key, abortAfter(fn))
}

func abortAfter(fn func(*sql.Tx) error) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		return errInjectedAbort
	}
}

// credentialFixture is a FileIO deployment with search on and no Redis: the
// Go service and its SQL database are the only dependencies.
type credentialFixture struct {
	*raceFixture
	clock    *movableClock
	embedder *recordingEmbedder
}

func newCredentialFixture(t *testing.T, backend string, configure func(*files.Settings)) *credentialFixture {
	t.Helper()
	f := newRaceFixture(t, backend, func(s *files.Settings) {
		s.Search.Enabled = true
		s.Security.CredentialCacheTTL = credentialTTL
		s.Index.SummaryModel = "" // no contextualizer model call
		s.Index.ChunkBytes = 64
		s.Index.BatchSize = 50
		s.Index.RetryMax = 0
		if configure != nil {
			configure(s)
		}
	})
	return &credentialFixture{
		raceFixture: f,
		clock:       &movableClock{now: time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)},
		embedder:    &recordingEmbedder{},
	}
}

// service builds a service on client's pool, as one process of a deployment.
func (c *credentialFixture) service(t *testing.T, client int, lock files.LockProvider) *files.Service {
	t.Helper()
	svc, err := files.NewService(c.db[client], c.settings, c.embedder, nil, nil, nil, lock, c.clock.Now)
	require.NoError(t, err)
	return svc
}

// tenant returns the verified identity of apiKey.
func (c *credentialFixture) tenant(t *testing.T, apiKey string) files.AuthContext {
	t.Helper()
	auth, ok := files.AuthFromVerified(authtest.Verified(apiKey))
	require.True(t, ok)
	return auth
}

// envelopeRow is one stored credential envelope.
type envelopeRow struct {
	key      string
	value    string
	expireAt time.Time
}

func (c *credentialFixture) envelopes(t *testing.T) []envelopeRow {
	t.Helper()
	rows, err := c.db[1].QueryContext(c.ctx, "SELECT key, value, expire_at FROM kv ORDER BY key")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var result []envelopeRow
	for rows.Next() {
		var row envelopeRow
		require.NoError(t, rows.Scan(&row.key, &row.value, &row.expireAt))
		result = append(result, row)
	}
	require.NoError(t, rows.Err())
	return result
}

// jobs counts the tenant's index jobs of one operation across every project.
func (c *credentialFixture) jobs(t *testing.T, auth files.AuthContext, operation string) int {
	t.Helper()
	var n int
	require.NoError(t, c.db[1].QueryRowContext(c.ctx, c.query(`SELECT COUNT(*) FROM mcp_file_index_jobs
		WHERE apikey_hash = ? AND operation = ? AND system_owner = ?`), auth.APIKeyHash, operation, "").Scan(&n))
	return n
}

// indexed counts the semantic and lexical index rows of one file.
func (c *credentialFixture) indexed(t *testing.T, auth files.AuthContext, project, path string) (chunks, embeddings int) {
	t.Helper()
	require.NoError(t, c.db[1].QueryRowContext(c.ctx, c.query(`SELECT COUNT(*) FROM mcp_file_chunks
		WHERE apikey_hash = ? AND project = ? AND file_path = ? AND system_owner = ?`), auth.APIKeyHash, project, path, "").Scan(&chunks))
	require.NoError(t, c.db[1].QueryRowContext(c.ctx, c.query(`SELECT COUNT(*) FROM mcp_file_chunk_embeddings e
		JOIN mcp_file_chunks c ON c.id = e.chunk_id
		WHERE c.apikey_hash = ? AND c.project = ? AND c.file_path = ? AND c.system_owner = ?`), auth.APIKeyHash, project, path, "").Scan(&embeddings))
	return chunks, embeddings
}

func runWorker(t *testing.T, ctx context.Context, svc *files.Service) {
	t.Helper()
	require.NoError(t, svc.NewIndexWorker().RunOnce(ctx))
}

// TestCredentialEnvelopesNeedOnlySQL verifies write, same-project rename and
// cross-project rename each hand the caller's key to the index worker through
// an encrypted, expiring envelope in the FileIO database, which the worker
// uses for semantic indexing and then deletes.
func TestCredentialEnvelopesNeedOnlySQL(t *testing.T) {
	for _, backend := range credentialTestBackends {
		t.Run(backend, func(t *testing.T) {
			c := newCredentialFixture(t, backend, nil)
			svc := c.service(t, 0, nil)

			_, err := svc.Write(c.ctx, c.auth, "race", "/doc.md", "semantic credential handoff", "utf-8", 0, files.WriteModeTruncate)
			require.NoError(t, err)
			stored := c.envelopes(t)
			require.Len(t, stored, 1)
			reference := fmt.Sprintf("%s|race|/doc.md|%d", c.auth.APIKeyHash, c.clock.Now().UnixNano())
			require.Equal(t, "mcp_files_credential_"+base64.RawURLEncoding.EncodeToString([]byte(reference)), stored[0].key,
				"the key encodes the credential reference")
			require.NotContains(t, stored[0].value, c.apiKey, "the envelope must be encrypted")
			require.NotContains(t, stored[0].key, c.apiKey, "the key never holds the caller's key")
			require.WithinDuration(t, c.clock.Now().Add(credentialTTL), stored[0].expireAt, time.Second)

			steps := []struct {
				name string
				run  func() error
				at   [2]string
			}{
				{"write", func() error { return nil }, [2]string{"race", "/doc.md"}},
				{"same-project rename", func() error {
					_, err := svc.Rename(c.ctx, c.auth, files.RenameRequest{Project: "race", FromPath: "/doc.md", ToPath: "/moved.md"})
					return err
				}, [2]string{"race", "/moved.md"}},
				{"cross-project rename", func() error {
					_, err := svc.Rename(c.ctx, c.auth, files.RenameRequest{Project: "race", FromPath: "/moved.md", TargetProject: "race-peer", ToPath: "/moved.md"})
					return err
				}, [2]string{"race-peer", "/moved.md"}},
			}
			for i, step := range steps {
				require.NoError(t, step.run(), step.name)
				require.Len(t, c.envelopes(t), 1, step.name)
				runWorker(t, c.ctx, svc)
				require.Empty(t, c.envelopes(t), "%s: the worker deletes the envelope it used", step.name)
				calls := c.embedder.Calls()
				require.Len(t, calls, i+1, step.name)
				require.Equal(t, c.apiKey, calls[i].apiKey, "%s: the worker embeds with the caller's key", step.name)
				chunks, embeddings := c.indexed(t, c.auth, step.at[0], step.at[1])
				require.Positive(t, chunks, step.name)
				require.Equal(t, chunks, embeddings, "%s: the destination is indexed semantically", step.name)
			}
		})
	}
}

// TestCredentialEnvelopeRollsBackWithTheMutation verifies a mutation that
// fails before commit leaves no envelope, no index job and no file change.
func TestCredentialEnvelopeRollsBackWithTheMutation(t *testing.T) {
	for _, backend := range credentialTestBackends {
		t.Run(backend, func(t *testing.T) {
			c := newCredentialFixture(t, backend, nil)
			svc := c.service(t, 0, nil)
			aborting := c.service(t, 0, abortBeforeCommit{})

			_, err := aborting.Write(c.ctx, c.auth, "race", "/new.md", "never committed", "utf-8", 0, files.WriteModeTruncate)
			require.ErrorIs(t, err, errInjectedAbort)
			require.Empty(t, c.envelopes(t))
			require.Zero(t, c.jobs(t, c.auth, "UPSERT"))
			_, err = svc.Read(c.ctx, c.auth, "race", "/new.md", 0, -1)
			require.True(t, files.IsCode(err, files.ErrCodeNotFound), "%v", err)

			_, err = svc.Write(c.ctx, c.auth, "race", "/kept.md", "committed bytes", "utf-8", 0, files.WriteModeTruncate)
			require.NoError(t, err)
			runWorker(t, c.ctx, svc)
			require.Empty(t, c.envelopes(t))
			upserts, deletes := c.jobs(t, c.auth, "UPSERT"), c.jobs(t, c.auth, "DELETE")

			for _, target := range []string{"race", "race-peer"} {
				_, err = aborting.Rename(c.ctx, c.auth, files.RenameRequest{Project: "race", FromPath: "/kept.md", TargetProject: target, ToPath: "/elsewhere.md"})
				require.ErrorIs(t, err, errInjectedAbort, target)
				require.Empty(t, c.envelopes(t), target)
				require.Equal(t, upserts, c.jobs(t, c.auth, "UPSERT"), target)
				require.Equal(t, deletes, c.jobs(t, c.auth, "DELETE"), target)
				read, err := svc.Read(c.ctx, c.auth, "race", "/kept.md", 0, -1)
				require.NoError(t, err, target)
				require.Equal(t, "committed bytes", read.Content, target)
			}
		})
	}
}

// TestCredentialEnvelopeExpiryDegradesAndRecovers verifies an expired envelope
// is swept, never blocks indexing, leaves the file lexically indexed, and that
// the next authenticated mutation restores semantic indexing.
func TestCredentialEnvelopeExpiryDegradesAndRecovers(t *testing.T) {
	for _, backend := range credentialTestBackends {
		t.Run(backend, func(t *testing.T) {
			c := newCredentialFixture(t, backend, nil)
			svc := c.service(t, 0, nil)

			_, err := svc.Write(c.ctx, c.auth, "race", "/late.md", "indexed after the envelope expired", "utf-8", 0, files.WriteModeTruncate)
			require.NoError(t, err)
			require.Len(t, c.envelopes(t), 1)
			c.clock.Advance(credentialTTL + time.Second)

			runWorker(t, c.ctx, svc)
			require.Empty(t, c.envelopes(t), "the worker sweeps the expired envelope")
			require.Empty(t, c.embedder.Calls(), "an expired envelope is never used")
			chunks, embeddings := c.indexed(t, c.auth, "race", "/late.md")
			require.Positive(t, chunks, "indexing degrades to lexical instead of failing")
			require.Zero(t, embeddings)

			_, err = svc.Write(c.ctx, c.auth, "race", "/late.md", " and then recovered", "utf-8", 0, files.WriteModeAppend)
			require.NoError(t, err)
			runWorker(t, c.ctx, svc)
			calls := c.embedder.Calls()
			require.Len(t, calls, 1)
			require.Equal(t, c.apiKey, calls[0].apiKey)
			chunks, embeddings = c.indexed(t, c.auth, "race", "/late.md")
			require.Positive(t, chunks)
			require.Equal(t, chunks, embeddings, "the next authenticated write restores semantic indexing")
		})
	}
}

// TestCredentialEnvelopeIsTenantScoped verifies tenants writing the same
// project and path at the same instant get separate envelopes, and that an
// envelope copied under another tenant's reference never decrypts.
func TestCredentialEnvelopeIsTenantScoped(t *testing.T) {
	for _, backend := range credentialTestBackends {
		t.Run(backend, func(t *testing.T) {
			c := newCredentialFixture(t, backend, nil)
			svc := c.service(t, 0, nil)
			alice := c.tenant(t, "sk-fileio-credential-alice")
			bob := c.tenant(t, "sk-fileio-credential-bob")

			_, err := svc.Write(c.ctx, alice, "race", "/same.md", "alpha belongs to alice", "utf-8", 0, files.WriteModeTruncate)
			require.NoError(t, err)
			aliceRows := c.envelopes(t)
			require.Len(t, aliceRows, 1)
			_, err = svc.Write(c.ctx, bob, "race", "/same.md", "bravo belongs to bob", "utf-8", 0, files.WriteModeTruncate)
			require.NoError(t, err)
			rows := c.envelopes(t)
			require.Len(t, rows, 2, "each tenant has its own envelope")
			var bobKey string
			for _, row := range rows {
				if row.key != aliceRows[0].key {
					bobKey = row.key
				}
			}
			require.NotEmpty(t, bobKey)
			_, err = c.db[1].ExecContext(c.ctx, c.query("UPDATE kv SET value = ? WHERE key = ?"), aliceRows[0].value, bobKey)
			require.NoError(t, err)

			runWorker(t, c.ctx, svc)
			for _, call := range c.embedder.Calls() {
				require.NotEqual(t, "sk-fileio-credential-bob", call.apiKey, "bob's envelope was replaced")
				for _, input := range call.inputs {
					require.NotContains(t, input, "bravo", "alice's key must never embed bob's file")
				}
			}
			_, aliceEmbeddings := c.indexed(t, alice, "race", "/same.md")
			require.Positive(t, aliceEmbeddings)
			bobChunks, bobEmbeddings := c.indexed(t, bob, "race", "/same.md")
			require.Positive(t, bobChunks, "bob's file is still indexed lexically")
			require.Zero(t, bobEmbeddings)
		})
	}
}

// TestCredentialEnvelopeOutlivesARetriedJob verifies a job whose embedding call
// fails keeps its envelope, and its retry embeds with the same caller key and
// then deletes it.
func TestCredentialEnvelopeOutlivesARetriedJob(t *testing.T) {
	for _, backend := range credentialTestBackends {
		t.Run(backend, func(t *testing.T) {
			c := newCredentialFixture(t, backend, func(s *files.Settings) {
				s.Index.RetryMax = 1
				s.Index.RetryBackoff = time.Second
			})
			c.embedder.failures = 1
			svc := c.service(t, 0, nil)
			_, err := svc.Write(c.ctx, c.auth, "race", "/retry.md", "embedded on the second attempt", "utf-8", 0, files.WriteModeTruncate)
			require.NoError(t, err)

			runWorker(t, c.ctx, svc)
			require.Len(t, c.embedder.Calls(), 1)
			require.Len(t, c.envelopes(t), 1, "a failed attempt keeps the envelope for its retry")

			c.clock.Advance(2 * time.Second)
			runWorker(t, c.ctx, svc)
			calls := c.embedder.Calls()
			require.Len(t, calls, 2)
			require.Equal(t, c.apiKey, calls[1].apiKey)
			require.Empty(t, c.envelopes(t))
			chunks, embeddings := c.indexed(t, c.auth, "race", "/retry.md")
			require.Positive(t, chunks)
			require.Equal(t, chunks, embeddings)
		})
	}
}

// TestCredentialEnvelopeSurvivesRestart verifies another service process on
// the same database, such as a restarted one, uses an envelope a previous
// process stored.
func TestCredentialEnvelopeSurvivesRestart(t *testing.T) {
	for _, backend := range credentialTestBackends {
		t.Run(backend, func(t *testing.T) {
			c := newCredentialFixture(t, backend, nil)
			_, err := c.service(t, 0, nil).Write(c.ctx, c.auth, "race", "/durable.md", "stored before the restart", "utf-8", 0, files.WriteModeTruncate)
			require.NoError(t, err)

			restarted := c.service(t, 1, nil)
			runWorker(t, c.ctx, restarted)
			calls := c.embedder.Calls()
			require.Len(t, calls, 1)
			require.Equal(t, c.apiKey, calls[0].apiKey)
			require.Empty(t, c.envelopes(t))
		})
	}
}

// TestRAGOptOutStoresNoCredential verifies a write or rename of a file that
// opted out of RAG indexing enqueues no upsert and stores no envelope.
func TestRAGOptOutStoresNoCredential(t *testing.T) {
	for _, backend := range credentialTestBackends {
		t.Run(backend, func(t *testing.T) {
			c := newCredentialFixture(t, backend, nil)
			svc := c.service(t, 0, nil)

			_, err := svc.WriteWith(c.ctx, c.auth, "race", "/private.md", "not for search", "utf-8", 0, files.WriteModeTruncate,
				files.WriteOpts{SkipRAGIndex: true})
			require.NoError(t, err)
			require.Empty(t, c.envelopes(t))
			require.Zero(t, c.jobs(t, c.auth, "UPSERT"))

			for _, req := range []files.RenameRequest{
				{Project: "race", FromPath: "/private.md", ToPath: "/still-private.md"},
				{Project: "race", FromPath: "/still-private.md", TargetProject: "race-peer", ToPath: "/still-private.md"},
			} {
				_, err = svc.Rename(c.ctx, c.auth, req)
				require.NoError(t, err)
				require.Empty(t, c.envelopes(t), req.Destination())
				require.Zero(t, c.jobs(t, c.auth, "UPSERT"), req.Destination())
			}
		})
	}
}

// TestCredentialEnvelopesWithoutEncryptionKeys verifies a deployment without
// encryption keys still writes and renames, stores no envelope, and indexes
// lexically.
func TestCredentialEnvelopesWithoutEncryptionKeys(t *testing.T) {
	for _, backend := range credentialTestBackends {
		t.Run(backend, func(t *testing.T) {
			c := newCredentialFixture(t, backend, func(s *files.Settings) {
				s.Security.EncryptionKEKs = map[uint16]string{}
			})
			svc := c.service(t, 0, nil)

			_, err := svc.Write(c.ctx, c.auth, "race", "/plain.md", "lexical only", "utf-8", 0, files.WriteModeTruncate)
			require.NoError(t, err)
			_, err = svc.Rename(c.ctx, c.auth, files.RenameRequest{Project: "race", FromPath: "/plain.md", ToPath: "/renamed.md"})
			require.NoError(t, err)
			_, err = svc.Rename(c.ctx, c.auth, files.RenameRequest{Project: "race", FromPath: "/renamed.md", TargetProject: "race-peer", ToPath: "/renamed.md"})
			require.NoError(t, err)
			require.Empty(t, c.envelopes(t))

			runWorker(t, c.ctx, svc)
			require.Empty(t, c.embedder.Calls())
			chunks, embeddings := c.indexed(t, c.auth, "race-peer", "/renamed.md")
			require.Positive(t, chunks)
			require.Zero(t, embeddings)
		})
	}
}

// TestCredentialEnvelopeMigrationUpgradesV1 verifies a database migrated by
// schema step 1 gains the envelope table on startup without replaying step 1.
func TestCredentialEnvelopeMigrationUpgradesV1(t *testing.T) {
	for _, backend := range credentialTestBackends {
		t.Run(backend, func(t *testing.T) {
			c := newCredentialFixture(t, backend, nil)
			db := c.db[0]
			for _, stmt := range []string{
				"DROP TABLE kv",
				"DROP INDEX mcp_files_system_owner_idx",
				"DELETE FROM mcp_file_schema_migrations",
				"INSERT INTO mcp_file_schema_migrations (version) VALUES (1)",
			} {
				_, err := db.ExecContext(c.ctx, stmt)
				require.NoError(t, err, stmt)
			}

			svc := c.service(t, 0, nil)
			_, err := svc.Write(c.ctx, c.auth, "race", "/upgraded.md", "after the upgrade", "utf-8", 0, files.WriteModeTruncate)
			require.NoError(t, err)
			require.Len(t, c.envelopes(t), 1)

			var version int64
			require.NoError(t, db.QueryRowContext(c.ctx, "SELECT MAX(version) FROM mcp_file_schema_migrations").Scan(&version))
			require.EqualValues(t, 2, version)
			indexQuery := "SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'mcp_files_system_owner_idx'"
			if c.postgres {
				indexQuery = "SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = 'mcp_files_system_owner_idx'"
			}
			var indexes int
			require.NoError(t, db.QueryRowContext(c.ctx, indexQuery).Scan(&indexes))
			require.Zero(t, indexes, "step 1 must not run again")
		})
	}
}
