package files_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
	ragplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/rag"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
)

// unselectedPlugin stands in for a second registered memory plugin. It shares
// no storage and records every operation, so any copy of a mutation that is
// not routed to the selected plugin shows up here.
type unselectedPlugin struct {
	mu    sync.Mutex
	calls []string
}

// record appends one operation name.
func (p *unselectedPlugin) record(op string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, op)
}

// recorded returns a copy of the recorded operation names.
func (p *unselectedPlugin) recorded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

// Name reports the PageIndex plugin name, the former shadow target.
func (p *unselectedPlugin) Name() string { return "unselected-test-backend" }

// Capabilities returns zero-value capabilities.
func (p *unselectedPlugin) Capabilities() mcpplugin.Capabilities { return mcpplugin.Capabilities{} }

// Start records the lifecycle call.
func (p *unselectedPlugin) Start(context.Context) error { p.record("start"); return nil }

// Stop records the lifecycle call.
func (p *unselectedPlugin) Stop(context.Context) error { p.record("stop"); return nil }

// Stat records the call.
func (p *unselectedPlugin) Stat(context.Context, files.AuthContext, string, string) (files.StatResult, error) {
	p.record("stat")
	return files.StatResult{}, nil
}

// Read records the call.
func (p *unselectedPlugin) Read(context.Context, files.AuthContext, string, string, int64, int64) (files.ReadResult, error) {
	p.record("read")
	return files.ReadResult{}, nil
}

// Write records the call.
func (p *unselectedPlugin) Write(context.Context, files.AuthContext, string, string, string, string, int64, files.WriteMode) (files.WriteResult, error) {
	p.record("write")
	return files.WriteResult{}, nil
}

// Delete records the call.
func (p *unselectedPlugin) Delete(context.Context, files.AuthContext, string, string, bool) (files.DeleteResult, error) {
	p.record("delete")
	return files.DeleteResult{}, nil
}

// Rename records the call.
func (p *unselectedPlugin) Rename(context.Context, files.AuthContext, files.RenameRequest) (files.RenameResult, error) {
	p.record("rename")
	return files.RenameResult{}, nil
}

// List records the call.
func (p *unselectedPlugin) List(context.Context, files.AuthContext, string, string, int, int) (files.ListResult, error) {
	p.record("list")
	return files.ListResult{}, nil
}

// Search records the call.
func (p *unselectedPlugin) Search(context.Context, files.AuthContext, string, string, string, int) (files.SearchResult, error) {
	p.record("search")
	return files.SearchResult{}, nil
}

// onceJob is one RAG outbox row as the index worker would consume it.
type onceJob struct {
	Operation   string
	Path        string
	ContentHash string
}

// onceSnapshot is one retained history row.
type onceSnapshot struct {
	Path    string
	Content string
}

// jobsSince lists the fixture tenant's outbox rows with an id above after.
func (f *raceFixture) jobsSince(t *testing.T, after int64) []onceJob {
	t.Helper()
	rows, err := f.db[1].QueryContext(f.ctx, f.query(`SELECT operation, file_path, content_hash FROM mcp_file_index_jobs
		WHERE apikey_hash = ? AND project = ? AND system_owner = ? AND id > ? ORDER BY id ASC`), f.auth.APIKeyHash, "race", "", after)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var jobs []onceJob
	for rows.Next() {
		var job onceJob
		var hash sql.NullString
		require.NoError(t, rows.Scan(&job.Operation, &job.Path, &hash))
		job.ContentHash = hash.String
		jobs = append(jobs, job)
	}
	require.NoError(t, rows.Err())
	return jobs
}

// lastJobID returns the highest outbox id of the fixture tenant, or zero.
func (f *raceFixture) lastJobID(t *testing.T) int64 {
	t.Helper()
	var id sql.NullInt64
	require.NoError(t, f.db[1].QueryRowContext(f.ctx, f.query(`SELECT MAX(id) FROM mcp_file_index_jobs WHERE apikey_hash = ? AND project = ? AND system_owner = ?`),
		f.auth.APIKeyHash, "race", "").Scan(&id))
	return id.Int64
}

// snapshots lists the fixture tenant's retained history rows in creation order.
func (f *raceFixture) snapshots(t *testing.T) []onceSnapshot {
	t.Helper()
	rows, err := f.db[1].QueryContext(f.ctx, f.query(`SELECT path, content FROM mcp_file_versions
		WHERE apikey_hash = ? AND project = ? AND system_owner = ? ORDER BY id ASC`), f.auth.APIKeyHash, "race", "")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var out []onceSnapshot
	for rows.Next() {
		var row onceSnapshot
		var content []byte
		require.NoError(t, rows.Scan(&row.Path, &content))
		row.Content = string(content)
		out = append(out, row)
	}
	require.NoError(t, rows.Err())
	return out
}

// TestFileToolMutationsExecuteOnce runs the SQLite variant of
// runFileToolMutationsExecuteOnce.
func TestFileToolMutationsExecuteOnce(t *testing.T) {
	runFileToolMutationsExecuteOnce(t, "sqlite")
}

// TestFileToolMutationsExecuteOncePostgres runs the PostgreSQL variant of
// runFileToolMutationsExecuteOnce; it skips without FILEIO_TEST_POSTGRES_DSN.
func TestFileToolMutationsExecuteOncePostgres(t *testing.T) {
	runFileToolMutationsExecuteOnce(t, "postgres")
}

// runFileToolMutationsExecuteOnce drives an APPEND, a rename and a delete
// through the MCP file tools and the plugin manager, registered the way
// startup registers them: the RAG plugin as the default next to another
// plugin. Each mutation must change the shared FileIO file exactly once, add
// exactly the expected history rows and RAG outbox jobs, leave the other
// plugin and another tenant untouched, and start no background work that
// changes anything after the manager stops.
func runFileToolMutationsExecuteOnce(t *testing.T, backend string) {
	t.Helper()
	// No index worker consumes the outbox, so the queued jobs stay observable
	// and the manager lifecycle starts nothing in the background.
	f := newRaceFixture(t, backend, func(s *files.Settings) { s.Index.Workers = 0 })

	// Another tenant owns the same project and path; nothing below may touch it.
	f.setTenant(t, "sk-fileio-once-other-tenant")
	f.write(t, 0, "/notes.md", "other", files.WriteModeTruncate, 0)
	otherJobs, otherSnapshots := f.count(t, "mcp_file_index_jobs"), f.count(t, "mcp_file_versions")
	f.setTenant(t, "sk-fileio-once-tenant")

	rag, err := ragplugin.New(f.svc[0])
	require.NoError(t, err)
	other := &unselectedPlugin{}
	manager, err := mcpplugin.NewManager(mcpplugin.DefaultPluginRAG, rag, other)
	require.NoError(t, err)
	require.NoError(t, manager.StartAll(f.ctx))
	writer, err := tools.NewFileWriteTool(manager)
	require.NoError(t, err)
	renamer, err := tools.NewFileRenameTool(manager)
	require.NoError(t, err)
	deleter, err := tools.NewFileDeleteTool(manager)
	require.NoError(t, err)
	ctx := f.toolContext()

	created := callRaceTool(t, ctx, writer, "file_write", map[string]any{"project": "race", "path": "notes.md", "content": "A", "create_only": true})
	f.assertStored(t, "/notes.md", "A")
	require.Empty(t, f.snapshots(t), "creating a file retains no history")

	// APPEND: the delta lands once, the previous generation is retained once,
	// and one upsert job carries the hash of the complete new content.
	lastJob := f.lastJobID(t)
	appended := callRaceTool(t, ctx, writer, "file_write", map[string]any{
		"project": "race", "path": "notes.md", "content": "B", "mode": "APPEND", "expected_version": created["version"],
	})
	require.EqualValues(t, 1, appended["bytes_written"])
	f.assertStored(t, "/notes.md", "AB")
	require.Equal(t, []onceSnapshot{{Path: "/notes.md", Content: "A"}}, f.snapshots(t))
	require.Equal(t, []onceJob{{Operation: "UPSERT", Path: "/notes.md", ContentHash: files.HashFileContent([]byte("AB"))}}, f.jobsSince(t, lastJob))

	// Rename: the file and its history move together; the old path is
	// deindexed and the new one indexed, once each.
	lastJob = f.lastJobID(t)
	moved := callRaceTool(t, ctx, renamer, "file_rename", map[string]any{
		"project": "race", "from_path": "notes.md", "to_path": "moved.md", "expected_version": appended["version"],
	})
	require.EqualValues(t, 1, moved["moved_count"])
	f.assertStored(t, "/moved.md", "AB")
	stat, err := f.svc[0].Stat(f.ctx, f.auth, "race", "/notes.md")
	require.NoError(t, err)
	require.False(t, stat.Exists)
	require.Equal(t, []onceSnapshot{{Path: "/moved.md", Content: "A"}}, f.snapshots(t))
	require.Equal(t, []onceJob{
		{Operation: "DELETE", Path: "/notes.md"},
		{Operation: "UPSERT", Path: "/moved.md", ContentHash: files.HashFileContent([]byte("AB"))},
	}, f.jobsSince(t, lastJob))

	// Delete: the deleted generation is retained once and one delete job is queued.
	current := readRaceVersion(t, f, "/moved.md")
	lastJob = f.lastJobID(t)
	deleted := callRaceTool(t, ctx, deleter, "file_delete", map[string]any{"project": "race", "path": "moved.md", "expected_version": current.Version})
	require.EqualValues(t, 1, deleted["deleted_count"])
	stat, err = f.svc[0].Stat(f.ctx, f.auth, "race", "/moved.md")
	require.NoError(t, err)
	require.False(t, stat.Exists)
	require.Equal(t, []onceSnapshot{{Path: "/moved.md", Content: "A"}, {Path: "/moved.md", Content: "AB"}}, f.snapshots(t))
	require.Equal(t, []onceJob{{Operation: "DELETE", Path: "/moved.md"}}, f.jobsSince(t, lastJob))

	// Stopping the manager is where queued background replays would drain.
	jobs, history := f.count(t, "mcp_file_index_jobs"), f.snapshots(t)
	require.NoError(t, manager.StopAll(f.ctx))
	require.Equal(t, []string{"start", "stop"}, other.recorded(), "no mutation reaches a plugin the call did not select")
	require.Equal(t, jobs, f.count(t, "mcp_file_index_jobs"))
	require.Equal(t, history, f.snapshots(t))

	f.setTenant(t, "sk-fileio-once-other-tenant")
	f.assertStored(t, "/notes.md", "other")
	require.Equal(t, otherJobs, f.count(t, "mcp_file_index_jobs"))
	require.Equal(t, otherSnapshots, f.count(t, "mcp_file_versions"))
}
