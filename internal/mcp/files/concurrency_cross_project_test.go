package files_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestFileIOPostgresOppositeCrossProjectMoves runs moves in opposite
// directions between two projects at the same time. Each move holds both
// project locks, so without one global lock order the two transactions would
// each hold one project and time out waiting for the other.
func TestFileIOPostgresOppositeCrossProjectMoves(t *testing.T) {
	f := newRaceFixture(t, "postgres", func(s *files.Settings) { s.LockTimeout = 3 * time.Second })
	projects := [2]string{"race", "race-peer"}
	const rounds = 12
	for round := range rounds {
		for i := range 2 {
			_, err := f.svc[i].Write(f.ctx, f.auth, projects[i], fmt.Sprintf("/move-%d.txt", round), projects[i], "utf-8", 0, files.WriteModeTruncate)
			require.NoError(t, err)
		}
	}
	for round := range rounds {
		path := fmt.Sprintf("/move-%d.txt", round)
		errs := parallelRaceCalls(t, f.ctx, 2, func(i int) error {
			_, err := f.svc[i].RenameAcrossProjects(f.ctx, f.auth, projects[i], path, projects[1-i], "/in"+path, false)
			return err
		})
		require.NoError(t, errs[0], "round %d", round)
		require.NoError(t, errs[1], "round %d", round)
	}
	for i := range 2 {
		for round := range rounds {
			read, err := f.svc[0].Read(f.ctx, f.auth, projects[1-i], fmt.Sprintf("/in/move-%d.txt", round), 0, -1)
			require.NoError(t, err)
			require.Equal(t, projects[i], read.Content)
		}
		listed, err := f.svc[0].List(f.ctx, f.auth, projects[i], "", 1, 100)
		require.NoError(t, err)
		require.Len(t, listed.Entries, 1, "only the incoming directory remains in %s", projects[i])
	}
}
