package files_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	errors "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestFileIOPostgresAppend checks every acknowledged record, byte boundary,
// historical preimage, and outbox generation through independent client pools.
func TestFileIOPostgresAppend(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%t", exists), func(t *testing.T) {
			f := newRaceFixture(t, "postgres", nil)
			const path = "/append.jsonl"
			if exists {
				f.write(t, 0, path, "", files.WriteModeTruncate, 0)
			}
			const n = 24
			records := make([]string, n)
			for i := range records {
				records[i] = fmt.Sprintf("{\"client\":%d,\"message\":\"漢🙂\"}\n", i)
			}
			for _, err := range parallelRaceCalls(t, f.ctx, n, func(i int) error {
				result, err := f.svc[i%2].Write(f.ctx, f.auth, "race", path, records[i], "utf-8", 999, files.WriteModeAppend)
				if err != nil {
					return err
				}
				if result.BytesWritten != int64(len(records[i])) {
					return errors.New("append acknowledged an incorrect byte count")
				}
				return nil
			}) {
				require.NoError(t, err)
			}
			got := f.read(t, 0, path)
			f.assertStored(t, path, got)
			require.True(t, utf8.ValidString(got))
			lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
			require.Len(t, lines, n)
			seen := map[int]bool{}
			prefixes := []string{""}
			for _, line := range lines {
				var record struct {
					Client  int    `json:"client"`
					Message string `json:"message"`
				}
				require.NoError(t, json.Unmarshal([]byte(line), &record))
				require.GreaterOrEqual(t, record.Client, 0)
				require.Less(t, record.Client, n)
				require.False(t, seen[record.Client], "duplicate append record")
				seen[record.Client] = true
				require.Equal(t, records[record.Client], line+"\n", "interleaved or corrupted record bytes")
				prefixes = append(prefixes, prefixes[len(prefixes)-1]+line+"\n")
			}
			start := 1 // First creation has no historical preimage.
			if exists {
				start = 0
			}
			f.assertLedger(t, path, prefixes[start:n], prefixes[start:n+1])
		})
	}
}

// TestFileIOPostgresDisjointOverwrite catches read/modify/write races that lose
// unrelated byte-range updates even though each individual SQL UPDATE is atomic.
func TestFileIOPostgresDisjointOverwrite(t *testing.T) {
	f := newRaceFixture(t, "postgres", nil)
	const n = 16
	slots := make([]string, n)
	for i := range slots {
		slots[i] = fmt.Sprintf("%02d=漢🙂|", i)
	}
	width := len(slots[0])
	f.write(t, 0, "/slots.txt", strings.Repeat(".", n*width), files.WriteModeTruncate, 0)
	for _, err := range parallelRaceCalls(t, f.ctx, n, func(i int) error {
		_, err := f.svc[i%2].Write(f.ctx, f.auth, "race", "/slots.txt", slots[i], "utf-8", int64(i*width), files.WriteModeOverwrite)
		return err
	}) {
		require.NoError(t, err)
	}
	f.assertStored(t, "/slots.txt", strings.Join(slots, ""))
	require.Equal(t, n, f.count(t, "mcp_file_versions"))
	require.Equal(t, n+1, f.count(t, "mcp_file_index_jobs"))
}

type raceMutation struct {
	mode    files.WriteMode
	content string
	offset  int
}

// serialRaceHistories is a small independent reference model, not production's
// applyWriteModeBytes. Each result includes all states, not merely final bytes.
func serialRaceHistories(initial string, ops []raceMutation) [][]string {
	if len(ops) == 0 {
		return [][]string{{initial}}
	}
	var histories [][]string
	for i, op := range ops {
		next := ""
		switch op.mode {
		case files.WriteModeAppend:
			next = initial + op.content
		case files.WriteModeTruncate:
			next = op.content
		case files.WriteModeOverwrite:
			if op.offset > len(initial) {
				continue
			}
			next = initial[:op.offset] + op.content
			if end := op.offset + len(op.content); end < len(initial) {
				next += initial[end:]
			}
		}
		rest := append([]raceMutation{}, ops[:i]...)
		rest = append(rest, ops[i+1:]...)
		for _, tail := range serialRaceHistories(next, rest) {
			histories = append(histories, append([]string{initial}, tail...))
		}
	}
	return histories
}

// TestFileIOPostgresMixedModes rejects histories incompatible with ANY serial
// execution of overlapping APPEND/OVERWRITE/TRUNCATE operations.
func TestFileIOPostgresMixedModes(t *testing.T) {
	f := newRaceFixture(t, "postgres", nil)
	ops := []raceMutation{
		{files.WriteModeAppend, "漢", 0},
		{files.WriteModeOverwrite, "XY", 1},
		{files.WriteModeTruncate, "123456", 0},
	}
	allowed := serialRaceHistories("abcdef", ops)
	require.Len(t, allowed, 6)
	for round := range 8 {
		path := fmt.Sprintf("/mixed-%d.txt", round)
		f.write(t, 0, path, "abcdef", files.WriteModeTruncate, 0)
		for _, err := range parallelRaceCalls(t, f.ctx, len(ops), func(i int) error {
			op := ops[i]
			_, err := f.svc[i%2].Write(f.ctx, f.auth, "race", path, op.content, "utf-8", int64(op.offset), op.mode)
			return err
		}) {
			require.NoError(t, err)
		}
		history := f.history(t, path)
		final := f.read(t, 0, path)
		history = append(history, final)
		require.Contains(t, allowed, history, "non-serial content history (lost, torn, or mixed write)")
		f.assertStored(t, path, final)
		f.assertLedger(t, path, history[:len(history)-1], history)
	}
}

// TestFileIOPostgresReaders checks complete committed generations during write
// contention, including a same-statement content/size/SHA-256 invariant.
func TestFileIOPostgresReaders(t *testing.T) {
	f := newRaceFixture(t, "postgres", nil)
	const perWriter = 12
	generations := make([]string, 2*perWriter+1)
	allowed := make(map[string]bool, len(generations))
	for i := range generations {
		generations[i] = fmt.Sprintf("BEGIN-%02d\n", i) + strings.Repeat(fmt.Sprintf("%02d漢🙂", i), 512) + fmt.Sprintf("\nEND-%02d", i)
		allowed[generations[i]] = true
	}
	f.write(t, 0, "/large.txt", generations[0], files.WriteModeTruncate, 0)
	for _, err := range parallelRaceCalls(t, f.ctx, 6, func(i int) error {
		if i < 2 {
			for j := range perWriter {
				_, err := f.svc[i].Write(f.ctx, f.auth, "race", "/large.txt", generations[1+i*perWriter+j], "utf-8", 0, files.WriteModeTruncate)
				if err != nil {
					return err
				}
			}
			return nil
		}
		for range 48 {
			result, err := f.svc[i%2].Read(f.ctx, f.auth, "race", "/large.txt", 0, -1)
			if err != nil {
				return err
			}
			if !allowed[result.Content] || !utf8.ValidString(result.Content) {
				return errors.New("reader observed torn, mixed, or uncommitted content")
			}
			_, content, err := f.stored("/large.txt")
			if err != nil {
				return err
			}
			if !allowed[content] {
				return errors.New("metadata row contains an unknown content generation")
			}
		}
		return nil
	}) {
		require.NoError(t, err)
	}
}

// TestFileIOPostgresTransactionBoundary deterministically holds an actual
// transaction before commit; no sleeps or mocked database locks order the test.
func TestFileIOPostgresTransactionBoundary(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback=%t", rollback), func(t *testing.T) {
			f := newRaceFixture(t, "postgres", nil)
			f.write(t, 0, "/atomic.txt", "old", files.WriteModeTruncate, 0)
			gate := &commitGate{prepared: make(chan struct{}), release: make(chan struct{})}
			t.Cleanup(gate.Release)
			if rollback {
				gate.abort = errors.New("injected failure after data, history, and outbox writes")
			}
			writer := f.service(t, 0, gate)
			f.settings.LockTimeout = 100 * time.Millisecond
			contender := f.service(t, 1, nil)
			done := make(chan error, 1)
			go func() {
				_, err := writer.Write(f.ctx, f.auth, "race", "/atomic.txt", "new", "utf-8", 0, files.WriteModeTruncate)
				done <- err
			}()
			select {
			case <-gate.prepared:
			case <-f.ctx.Done():
				t.Fatal("writer never reached the pre-commit boundary")
			}
			// Reader is on the other pool while the writer's transaction is open.
			require.Equal(t, "old", f.read(t, 1, "/atomic.txt"))
			f.assertStored(t, "/atomic.txt", "old")
			require.Equal(t, 0, f.count(t, "mcp_file_versions"))
			require.Equal(t, 1, f.count(t, "mcp_file_index_jobs"))
			_, busyErr := contender.Write(f.ctx, f.auth, "race", "/atomic.txt", "blocked", "utf-8", 0, files.WriteModeAppend)
			require.True(t, files.IsCode(busyErr, files.ErrCodeResourceBusy), "independent writer must encounter the real held lock: %v", busyErr)
			gate.Release()
			select {
			case err := <-done:
				if rollback {
					require.ErrorIs(t, err, gate.abort)
					f.assertStored(t, "/atomic.txt", "old")
					require.Equal(t, 0, f.count(t, "mcp_file_versions"))
					require.Equal(t, 1, f.count(t, "mcp_file_index_jobs"))
				} else {
					require.NoError(t, err)
					f.assertStored(t, "/atomic.txt", "new")
					f.assertLedger(t, "/atomic.txt", []string{"old"}, []string{"old", "new"})
				}
			case <-f.ctx.Done():
				t.Fatal("writer failed to finish after release")
			}
			// A failed contender must not leak a connection or advisory lock.
			_, err := contender.Write(f.ctx, f.auth, "race", "/atomic.txt", "after", "utf-8", 0, files.WriteModeTruncate)
			require.NoError(t, err)
			f.assertStored(t, "/atomic.txt", "after")
		})
	}
}

// TestFileIOPostgresNamespaceAndQuota covers the invariants that a per-file-only
// replacement lock would lose: parent/child exclusion and project-wide quota.
func TestFileIOPostgresNamespaceAndQuota(t *testing.T) {
	t.Run("parent_vs_child", func(t *testing.T) {
		f := newRaceFixture(t, "postgres", nil)
		paths := []string{"/node", "/node/child.txt"}
		errs := parallelRaceCalls(t, f.ctx, 2, func(i int) error {
			_, err := f.svc[i].Write(f.ctx, f.auth, "race", paths[i], "data", "utf-8", 0, files.WriteModeTruncate)
			return err
		})
		require.NotEqual(t, errs[0] == nil, errs[1] == nil, "exactly one namespace interpretation may win")
		for _, err := range errs {
			if err != nil {
				require.True(t, files.IsCode(err, files.ErrCodeIsDirectory) || files.IsCode(err, files.ErrCodeNotDirectory), "%v", err)
			}
		}
		require.Equal(t, 1, f.count(t, "mcp_files"))
		require.Equal(t, 1, f.count(t, "mcp_file_index_jobs"))
	})
	t.Run("quota_across_different_paths", func(t *testing.T) {
		f := newRaceFixture(t, "postgres", func(s *files.Settings) { s.MaxProjectBytes = 10 })
		errs := parallelRaceCalls(t, f.ctx, 2, func(i int) error {
			_, err := f.svc[i].Write(f.ctx, f.auth, "race", fmt.Sprintf("/quota-%d.txt", i), "123456", "utf-8", 0, files.WriteModeTruncate)
			return err
		})
		require.NotEqual(t, errs[0] == nil, errs[1] == nil)
		for _, err := range errs {
			if err != nil {
				require.True(t, files.IsCode(err, files.ErrCodeQuotaExceeded), "%v", err)
			}
		}
		require.Equal(t, 1, f.count(t, "mcp_files"))
		require.Equal(t, 1, f.count(t, "mcp_file_index_jobs"))
		require.Equal(t, 0, f.count(t, "mcp_file_versions"))
	})
	t.Run("competing_rename_destinations", func(t *testing.T) {
		f := newRaceFixture(t, "postgres", nil)
		paths := []string{"/left.txt", "/right.txt"}
		for i, path := range paths {
			f.write(t, i, path, path, files.WriteModeTruncate, 0)
		}
		errs := parallelRaceCalls(t, f.ctx, 2, func(i int) error {
			_, err := f.svc[i].Rename(f.ctx, f.auth, "race", paths[i], "/destination.txt", false)
			return err
		})
		require.NotEqual(t, errs[0] == nil, errs[1] == nil)
		for i, err := range errs {
			if err == nil {
				f.assertStored(t, "/destination.txt", paths[i])
				_, _, lookupErr := f.stored(paths[i])
				require.ErrorIs(t, lookupErr, sql.ErrNoRows)
			} else {
				require.True(t, files.IsCode(err, files.ErrCodeAlreadyExists), "%v", err)
				f.assertStored(t, paths[i], paths[i])
			}
		}
		require.Equal(t, 4, f.count(t, "mcp_file_index_jobs")) // two creates + rename DELETE/UPSERT
	})
}

func (f *raceFixture) history(t *testing.T, path string) []string {
	t.Helper()
	versions, err := f.svc[0].ListVersions(f.ctx, f.auth, "race", path)
	require.NoError(t, err)
	result := make([]string, 0, len(versions))
	for i := len(versions) - 1; i >= 0; i-- {
		version, err := f.svc[0].ReadVersion(f.ctx, f.auth, "race", path, versions[i].ID)
		require.NoError(t, err)
		require.Equal(t, int64(len(version.Content)), version.Size)
		result = append(result, string(version.Content))
	}
	return result
}

func (f *raceFixture) assertLedger(t *testing.T, path string, preimages, generations []string) {
	t.Helper()
	require.Equal(t, preimages, f.history(t, path), "history lost or mixed a committed preimage")
	rows, err := f.db[1].QueryContext(f.ctx, f.query(`SELECT content_hash FROM mcp_file_index_jobs
		WHERE apikey_hash = ? AND project = ? AND file_path = ? AND system_owner = ? AND operation = 'UPSERT' ORDER BY id`),
		f.auth.APIKeyHash, "race", path, "")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var hashes []string
	for rows.Next() {
		var hash string
		require.NoError(t, rows.Scan(&hash))
		hashes = append(hashes, hash)
	}
	require.NoError(t, rows.Err())
	expected := make([]string, len(generations))
	for i, generation := range generations {
		expected[i] = files.HashFileContent([]byte(generation))
	}
	require.Equal(t, expected, hashes, "outbox must describe the exact serialized content generations")
}
