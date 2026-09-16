package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

type fileIORaceHandler interface {
	Handle(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
}

type fileIORaceClient struct {
	ctx   context.Context
	svc   *files.Service
	tools map[string]fileIORaceHandler
}

type fileIORaceHarness struct {
	clients  []fileIORaceClient
	db       *sql.DB
	postgres bool
	auth     files.AuthContext
	project  string
}

type fileIORaceReply struct {
	payload map[string]any
	failed  bool
	err     error
}

// newFileIORaceHarness uses real tool handlers, real services, and the production
// LockProvider. PostgreSQL clients use independent connection pools. The SQLite
// fallback deliberately shares one connection; it tests behavior, NOT the
// cross-process advisory-lock guarantee. CI must set FILEIO_TEST_POSTGRES_DSN.
func newFileIORaceHarness(t *testing.T, configure func(*files.Settings)) *fileIORaceHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	h := &fileIORaceHarness{
		auth:    files.AuthContext{APIKeyHash: "race-" + uuid.NewString(), UserIdentity: "test:race"},
		project: "race",
	}
	settings := files.LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Index.FileSummary.Enabled = false
	settings.Security.EncryptionKEKs = nil
	settings.MaxPayloadBytes = 1 << 20
	settings.MaxFileBytes = 2 << 20
	settings.MaxProjectBytes = 16 << 20
	settings.LockTimeout = 15 * time.Second
	if configure != nil {
		configure(&settings)
	}

	dsn := os.Getenv("FILEIO_TEST_POSTGRES_DSN")
	if os.Getenv("FILEIO_REQUIRE_POSTGRES") == "1" {
		require.NotEmpty(t, dsn, "PostgreSQL acceptance must not silently run the SQLite fallback")
	}
	var openDB func() *sql.DB
	if dsn != "" {
		h.postgres = true
		cfg, err := pgx.ParseConfig(dsn)
		require.NoError(t, err)
		admin := stdlib.OpenDB(*cfg)
		require.NoError(t, admin.PingContext(ctx))
		schema := "fileio_race_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		quoted := pgx.Identifier{schema}.Sanitize()
		_, err = admin.ExecContext(ctx, "CREATE SCHEMA "+quoted)
		require.NoError(t, err)
		t.Cleanup(func() {
			cleanupCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
			defer done()
			_, dropErr := admin.ExecContext(cleanupCtx, "DROP SCHEMA "+quoted+" CASCADE")
			require.NoError(t, dropErr)
			require.NoError(t, admin.Close())
		})
		cfg.RuntimeParams["search_path"] = schema + ",public"
		cfg.RuntimeParams["default_transaction_isolation"] = "read committed"
		cfg.RuntimeParams["statement_timeout"] = "20000"
		openDB = func() *sql.DB {
			db := stdlib.OpenDB(*cfg.Copy())
			db.SetMaxOpenConns(16)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			return db
		}
	} else {
		db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "race.db")+"?_busy_timeout=5000")
		require.NoError(t, err)
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { require.NoError(t, db.Close()) })
		openDB = func() *sql.DB { return db }
	}

	for range 3 {
		db := openDB()
		svc, err := files.NewService(db, settings, nil, nil, nil, nil, nil, nil, nil)
		require.NoError(t, err)
		plugin := mustE2EPlugin(t, svc)
		write, err := NewFileWriteTool(plugin)
		require.NoError(t, err)
		read, err := NewFileReadTool(plugin)
		require.NoError(t, err)
		stat, err := NewFileStatTool(plugin)
		require.NoError(t, err)
		del, err := NewFileDeleteTool(plugin)
		require.NoError(t, err)
		rename, err := NewFileRenameTool(plugin)
		require.NoError(t, err)
		auth := h.auth
		h.clients = append(h.clients, fileIORaceClient{
			ctx: context.WithValue(ctx, ctxkeys.AuthContext, &auth), svc: svc,
			tools: map[string]fileIORaceHandler{"write": write, "read": read, "stat": stat, "delete": del, "rename": rename},
		})
		if h.db == nil {
			h.db = db
		}
	}
	t.Logf("backend_postgres=%t; clients=3; independent_postgres_pools=%t; production_lock=true", h.postgres, h.postgres)
	return h
}

// call crosses the real MCP result JSON boundary. No testing.Fatal/require call
// is allowed here because concurrent operations run in worker goroutines.
func (h *fileIORaceHarness) call(client int, operation string, args map[string]any) fileIORaceReply {
	c := h.clients[client%len(h.clients)]
	if _, ok := args["project"]; !ok {
		args["project"] = h.project
	}
	res, err := c.tools[operation].Handle(c.ctx, newToolReq(args))
	if err != nil {
		return fileIORaceReply{err: errors.Wrap(err, "invoke MCP tool")}
	}
	if res == nil {
		return fileIORaceReply{err: errors.New("nil MCP result")}
	}
	wire, err := json.Marshal(res)
	if err != nil {
		return fileIORaceReply{err: errors.Wrap(err, "serialize MCP result")}
	}
	var decoded struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		return fileIORaceReply{err: errors.Wrap(err, "decode MCP result")}
	}
	for _, content := range decoded.Content {
		if content.Type != "text" {
			continue
		}
		payload := map[string]any{}
		if err := json.Unmarshal([]byte(content.Text), &payload); err != nil {
			return fileIORaceReply{err: errors.Wrap(err, "decode tool payload")}
		}
		return fileIORaceReply{payload: payload, failed: decoded.IsError}
	}
	return fileIORaceReply{err: errors.New("missing JSON text payload")}
}

// fileIORaceTogether starts every operation behind a barrier and joins all
// workers before any assertions; distinct result slots avoid test-side races.
func fileIORaceTogether(operations ...func() fileIORaceReply) []fileIORaceReply {
	start := make(chan struct{})
	results := make([]fileIORaceReply, len(operations))
	var ready, done sync.WaitGroup
	ready.Add(len(operations))
	done.Add(len(operations))
	for i, operation := range operations {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			results[i] = operation()
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	return results
}

func fileIORaceOK(t *testing.T, r fileIORaceReply) map[string]any {
	t.Helper()
	require.NoError(t, r.err)
	require.False(t, r.failed, "tool error: %#v", r.payload)
	return r.payload
}

func (h *fileIORaceHarness) write(client int, path, content, mode string, offset int) fileIORaceReply {
	return h.call(client, "write", map[string]any{"path": path, "content": content, "mode": mode, "offset": offset})
}

func (h *fileIORaceHarness) read(t *testing.T, path string) string {
	t.Helper()
	payload := fileIORaceOK(t, h.call(2, "read", map[string]any{"path": path}))
	content, ok := payload["content"].(string)
	require.True(t, ok)
	return content
}

// assertStored checks raw storage, not just JSON (which replaces invalid UTF-8).
func (h *fileIORaceHarness) assertStored(t *testing.T, path, expected string) {
	t.Helper()
	query := `SELECT content, size, content_hash FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path = ? AND deleted = FALSE AND system_owner = ?`
	if h.postgres {
		for i := 1; i <= 4; i++ {
			query = strings.Replace(query, "?", fmt.Sprintf("$%d", i), 1)
		}
	}
	var content []byte
	var size int64
	var hash string
	require.NoError(t, h.db.QueryRowContext(h.clients[0].ctx, query, h.auth.APIKeyHash, h.project, path, "").Scan(&content, &size, &hash))
	require.Equal(t, []byte(expected), content)
	require.Equal(t, int64(len(content)), size)
	require.Equal(t, files.HashFileContent(content), hash)
	require.Equal(t, expected, h.read(t, path))
}
