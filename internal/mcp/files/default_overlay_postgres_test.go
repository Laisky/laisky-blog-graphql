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
	"github.com/pgvector/pgvector-go"
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
			_, err := f.svc[i%2].Rename(f.ctx, f.auth, files.RenameRequest{Project: project, FromPath: source, ToPath: fmt.Sprintf("/moved/%d.md", i), Overwrite: false})
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

// keywordPostgresEmbedder embeds "needle" texts on one axis and every other
// text on another, at the production vector dimension.
type keywordPostgresEmbedder struct{}

// EmbedTexts returns one 1536-dimension keyword vector per input.
func (keywordPostgresEmbedder) EmbedTexts(_ context.Context, _ string, inputs []string) ([]pgvector.Vector, error) {
	vectors := make([]pgvector.Vector, 0, len(inputs))
	for _, input := range inputs {
		values := make([]float32, 1536)
		if strings.Contains(input, "needle") {
			values[0] = 1
		} else {
			values[1] = 1
		}
		vectors = append(vectors, pgvector.NewVector(values))
	}
	return vectors, nil
}

// TestDefaultOverlayPostgresSearch runs physical retrieval on PostgreSQL: the
// raw-file fallback, tsvector and pgvector retrieval, and the wildcard. A
// project searches its own files and every default, including the defaults its
// view hides, and every hit names its physical project.
func TestDefaultOverlayPostgresSearch(t *testing.T) {
	f := newRaceFixture(t, "postgres", func(s *files.Settings) {
		s.Search.Enabled = true
		s.Index.BatchSize = 50
	})
	newService := func(embedder files.Embedder, settings files.Settings) *files.Service {
		svc, err := files.NewService(f.db[0], settings, embedder, nil, nil, nil, nil, nil)
		require.NoError(t, err)
		return svc
	}
	svc := newService(nil, f.settings)
	write := func(project, path, content string) {
		_, err := svc.Write(f.ctx, f.auth, project, path, content, "utf-8", 0, files.WriteModeTruncate)
		require.NoError(t, err)
	}
	// The project file predates the default subtree it later hides.
	write("alpha", "/sub", "needle alpha file hides the default subtree")
	write(files.DefaultProject, "/u_1.md", "needle default override target")
	write(files.DefaultProject, "/only.md", "needle default only")
	write(files.DefaultProject, "/sub/deep.md", "needle default deep")
	write("alpha", "/note.md", "needle alpha note")
	// Override a default through the public recipe: a project file renamed onto
	// the default's name shadows it instead of deleting it.
	write("alpha", "/tmp-u.md", "needle alpha override")
	_, err := svc.Rename(f.ctx, f.auth, files.RenameRequest{Project: "alpha", FromPath: "/tmp-u.md", ToPath: "/u_1.md", Overwrite: true})
	require.NoError(t, err)

	search := func(svc *files.Service, project, prefix string) map[string]bool {
		t.Helper()
		res, err := svc.Search(f.ctx, f.auth, project, "needle", prefix, 20)
		require.NoError(t, err)
		out := map[string]bool{}
		for _, chunk := range res.Chunks {
			out[chunk.Project+":"+chunk.FilePath] = true
		}
		return out
	}
	defaults := map[string]bool{"@DEFAULT:/u_1.md": true, "@DEFAULT:/only.md": true, "@DEFAULT:/sub/deep.md": true}
	want := map[string]bool{
		"@DEFAULT:/u_1.md": true, "@DEFAULT:/only.md": true, "@DEFAULT:/sub/deep.md": true,
		"alpha:/note.md": true, "alpha:/u_1.md": true, "alpha:/sub": true,
	}
	require.Equal(t, want, search(svc, "alpha", ""), "raw-file fallback before indexing")

	require.NoError(t, svc.NewIndexWorker().RunOnce(f.ctx))
	var defaultChunks int
	require.NoError(t, f.db[0].QueryRowContext(f.ctx, f.query(
		`SELECT COUNT(1) FROM mcp_file_chunks WHERE project = ? AND system_owner = ''`), files.DefaultProject).Scan(&defaultChunks))
	require.Positive(t, defaultChunks, "defaults are indexed under their own physical project")
	require.Equal(t, want, search(svc, "alpha", ""), "indexed tsvector retrieval")
	require.Equal(t, map[string]bool{"@DEFAULT:/u_1.md": true, "alpha:/u_1.md": true}, search(svc, "alpha", "/u_"),
		"both documents at one path are hits, and the prefix is literal")
	require.Empty(t, search(svc, "alpha", "/U_"), "the prefix is case-sensitive")
	require.Equal(t, defaults, search(svc, "beta", ""))
	require.Equal(t, want, search(svc, files.ProjectWildcard, ""))

	// pgvector retrieval has the same physical scope.
	vectorSettings := f.settings
	vectorSettings.Search.LexicalCandidates = 0
	vector := newService(keywordPostgresEmbedder{}, vectorSettings)
	_, err = vector.Write(f.ctx, f.auth, files.DefaultProject, "/vector.md", "needle default vector", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	_, err = vector.Write(f.ctx, f.auth, "gamma", "/tmp-vector.md", "needle gamma vector", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	_, err = vector.Rename(f.ctx, f.auth, files.RenameRequest{Project: "gamma", FromPath: "/tmp-vector.md", ToPath: "/vector.md", Overwrite: true})
	require.NoError(t, err)
	_, err = vector.Write(f.ctx, f.auth, "gamma", "/plain.md", "unrelated words", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	require.NoError(t, vector.NewIndexWorker().RunOnce(f.ctx))
	res, err := vector.Search(f.ctx, f.auth, "gamma", "needle", "/vector", 2)
	require.NoError(t, err)
	var vectorHits []string
	for _, chunk := range res.Chunks {
		vectorHits = append(vectorHits, chunk.Project+":"+chunk.FilePath)
	}
	require.ElementsMatch(t, []string{"gamma:/vector.md", "@DEFAULT:/vector.md"}, vectorHits)
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
