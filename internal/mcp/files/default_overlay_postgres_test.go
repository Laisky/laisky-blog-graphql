package files_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestDefaultOverlayPostgresConcurrentWriteThrough appends to one default
// through many projects and directly, from two client pools, while unrelated
// project-local writes run in the same projects. Every append must land exactly
// once in the default row, no project may gain a copy, and lock ordering must
// not deadlock or time out.
func TestDefaultOverlayPostgresConcurrentWriteThrough(t *testing.T) {
	f := newRaceFixture(t, "postgres", nil)
	const path = "/shared.log"
	_, err := f.svc[0].Write(f.ctx, f.auth, files.DefaultProject, path, "", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)

	projects := []string{files.DefaultProject, "alpha", "beta", "gamma"}
	const n = 32
	for _, err := range parallelRaceCalls(t, f.ctx, n, func(i int) error {
		project := projects[i%len(projects)]
		record := fmt.Sprintf("record-%02d\n", i)
		res, err := f.svc[i%2].Write(f.ctx, f.auth, project, path, record, "utf-8", 0, files.WriteModeAppend)
		if err != nil {
			return err
		}
		if res.ResolvedProject != files.DefaultProject {
			return errors.Errorf("append through %s stored in %s", project, res.ResolvedProject)
		}
		// A project-local write in the same project takes the single-project path.
		if project != files.DefaultProject {
			local, err := f.svc[(i+1)%2].Write(f.ctx, f.auth, project, fmt.Sprintf("/local-%02d.txt", i), record, "utf-8", 0, files.WriteModeTruncate)
			if err != nil {
				return err
			}
			if local.ResolvedProject != project {
				return errors.Errorf("local write stored in %s", local.ResolvedProject)
			}
		}
		return nil
	}) {
		require.NoError(t, err)
	}

	got, err := f.svc[0].Read(f.ctx, f.auth, files.DefaultProject, path, 0, -1)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSuffix(got.Content, "\n"), "\n")
	require.Len(t, lines, n)
	seen := map[string]bool{}
	for _, line := range lines {
		require.False(t, seen[line], "duplicate append %s", line)
		seen[line] = true
	}
	for _, project := range projects[1:] {
		via, err := f.svc[1].Read(f.ctx, f.auth, project, path, 0, -1)
		require.NoError(t, err)
		require.Equal(t, got.Version, via.Version, "%s must read the default row itself", project)
	}
	var copies int
	require.NoError(t, f.db[0].QueryRowContext(f.ctx, f.query(
		`SELECT COUNT(1) FROM mcp_files WHERE path = ? AND deleted = FALSE AND system_owner = '' AND project <> ?`),
		path, files.DefaultProject).Scan(&copies))
	require.Zero(t, copies)
}

// TestDefaultOverlayPostgresCreateRace races a default's creation against a
// project's create_only write of the same name. Either order is valid, but the
// outcome must be one of the two serial results: the project created its own
// file before the default existed, or it saw the default and conflicted.
func TestDefaultOverlayPostgresCreateRace(t *testing.T) {
	for round := range 8 {
		t.Run(fmt.Sprintf("round-%d", round), func(t *testing.T) {
			f := newRaceFixture(t, "postgres", nil)
			path := fmt.Sprintf("/race-%d.md", round)
			errs := parallelRaceCalls(t, f.ctx, 2, func(i int) error {
				if i == 0 {
					_, err := f.svc[0].WriteWith(f.ctx, f.auth, files.DefaultProject, path, "default", "utf-8", 0, files.WriteModeTruncate, files.WriteOpts{CreateOnly: true})
					return err
				}
				_, err := f.svc[1].WriteWith(f.ctx, f.auth, "alpha", path, "alpha", "utf-8", 0, files.WriteModeTruncate, files.WriteOpts{CreateOnly: true})
				return err
			})
			require.NoError(t, errs[0], "the default layer itself is physical")
			read, err := f.svc[0].Read(f.ctx, f.auth, "alpha", path, 0, -1)
			require.NoError(t, err)
			if errs[1] == nil {
				require.Equal(t, "alpha", read.Content, "a project file created first shadows the later default")
			} else {
				require.True(t, files.IsCode(errs[1], files.ErrCodeVersionConflict), "unexpected error %v", errs[1])
				require.Equal(t, "default", read.Content)
			}
			other, err := f.svc[1].Read(f.ctx, f.auth, "beta", path, 0, -1)
			require.NoError(t, err)
			require.Equal(t, "default", other.Content)
		})
	}
}

// TestDefaultOverlayPostgresOppositeMutationsDoNotDeadlock runs overlay
// mutations of two projects and direct default mutations concurrently. All of
// them lock in ProjectLockOrder, so none may deadlock or exhaust its timeout.
func TestDefaultOverlayPostgresOppositeMutationsDoNotDeadlock(t *testing.T) {
	f := newRaceFixture(t, "postgres", nil)
	for i := range 8 {
		_, err := f.svc[0].Write(f.ctx, f.auth, files.DefaultProject, fmt.Sprintf("/d/%d.md", i), "seed", "utf-8", 0, files.WriteModeTruncate)
		require.NoError(t, err)
	}
	const n = 24
	for _, err := range parallelRaceCalls(t, f.ctx, n, func(i int) error {
		project := []string{"alpha", "beta", files.DefaultProject}[i%3]
		source := fmt.Sprintf("/d/%d.md", i%8)
		switch i % 4 {
		case 0, 1:
			_, err := f.svc[i%2].Write(f.ctx, f.auth, project, source, "x", "utf-8", 0, files.WriteModeAppend)
			if files.IsCode(err, files.ErrCodeNotFound) {
				return nil
			}
			return err
		case 2:
			_, err := f.svc[i%2].Rename(f.ctx, f.auth, project, source, fmt.Sprintf("/moved/%d.md", i), false)
			if files.IsCode(err, files.ErrCodeNotFound) || files.IsCode(err, files.ErrCodeAlreadyExists) {
				return nil
			}
			return err
		default:
			_, err := f.svc[i%2].List(f.ctx, f.auth, project, "", 3, 100)
			return err
		}
	}) {
		require.NoError(t, err)
	}
	listed, err := f.svc[0].List(f.ctx, f.auth, "alpha", "", 3, 100)
	require.NoError(t, err)
	var defaults int
	for _, entry := range listed.Entries {
		if entry.Type == files.FileTypeFile {
			defaults++
		}
	}
	require.Positive(t, defaults)
}

// TestDefaultOverlayPostgresSearch runs the overlay visibility predicate on
// PostgreSQL: tsvector retrieval, the raw-file fallback, and the wildcard.
func TestDefaultOverlayPostgresSearch(t *testing.T) {
	f := newRaceFixture(t, "postgres", func(s *files.Settings) {
		s.Search.Enabled = true
		s.Index.BatchSize = 50
	})
	svc, err := files.NewService(f.db[0], f.settings, nil, nil, nil, &overlayCredentialStore{data: map[string]string{}}, nil, nil, nil)
	require.NoError(t, err)
	write := func(project, path, content string) {
		_, err := svc.Write(f.ctx, f.auth, project, path, content, "utf-8", 0, files.WriteModeTruncate)
		require.NoError(t, err)
	}
	write(files.DefaultProject, "/u_1.md", "needle default override target")
	write(files.DefaultProject, "/only.md", "needle default only")
	write(files.DefaultProject, "/sub/deep.md", "needle default deep")
	write("alpha", "/note.md", "needle alpha note")
	// Hide the defaults through project files that predate them: rename a project
	// file over the default name (overwrite shadows instead of deleting).
	write("alpha", "/tmp-u.md", "needle alpha override")
	_, err = svc.Rename(f.ctx, f.auth, "alpha", "/tmp-u.md", "/u_1.md", true)
	require.NoError(t, err)

	search := func(project string) map[string]bool {
		t.Helper()
		res, err := svc.Search(f.ctx, f.auth, project, "needle", "", 20)
		require.NoError(t, err)
		out := map[string]bool{}
		for _, chunk := range res.Chunks {
			out[chunk.Project+":"+chunk.FilePath] = true
		}
		return out
	}
	want := map[string]bool{":/u_1.md": true, ":/only.md": true, ":/sub/deep.md": true, ":/note.md": true}
	require.Equal(t, want, search("alpha"), "raw-file fallback before indexing")

	require.NoError(t, svc.NewIndexWorker().RunOnce(f.ctx))
	var defaultChunks int
	require.NoError(t, f.db[0].QueryRowContext(f.ctx, f.query(
		`SELECT COUNT(1) FROM mcp_file_chunks WHERE project = ? AND system_owner = ''`), files.DefaultProject).Scan(&defaultChunks))
	require.Positive(t, defaultChunks, "defaults are indexed under their own physical project")
	require.Equal(t, want, search("alpha"), "indexed lexical retrieval")
	res, err := svc.Search(f.ctx, f.auth, "alpha", "override", "/u_", 20)
	require.NoError(t, err)
	require.Len(t, res.Chunks, 1)
	require.Contains(t, res.Chunks[0].ChunkContent, "alpha override", "the project file, not the default, answers")
	require.Equal(t, map[string]bool{":/u_1.md": true, ":/only.md": true, ":/sub/deep.md": true}, search("beta"))
	require.Equal(t, map[string]bool{
		"@DEFAULT:/u_1.md": true, "@DEFAULT:/only.md": true, "@DEFAULT:/sub/deep.md": true,
		"alpha:/u_1.md": true, "alpha:/note.md": true,
	}, search(files.ProjectWildcard))
}

// overlayCredentialStore keeps credential envelopes in memory for indexing tests.
type overlayCredentialStore struct {
	mu   sync.Mutex
	data map[string]string
}

// Store keeps one envelope.
func (s *overlayCredentialStore) Store(_ context.Context, key, payload string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = payload
	return nil
}

// Load returns one envelope or sql.ErrNoRows.
func (s *overlayCredentialStore) Load(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	payload, ok := s.data[key]
	if !ok {
		return "", sql.ErrNoRows
	}
	return payload, nil
}

// Delete removes one envelope.
func (s *overlayCredentialStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}
