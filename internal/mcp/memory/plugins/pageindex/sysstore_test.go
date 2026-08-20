package pageindex

import (
	"context"
	"sync"
	"testing"

	errors "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// inMemoryFS is a SystemFS stub for unit tests.
type inMemoryFS struct {
	mu   sync.Mutex
	data map[string]map[string][]byte
}

func newMemoryFS() *inMemoryFS {
	return &inMemoryFS{data: map[string]map[string][]byte{}}
}

func (m *inMemoryFS) Read(_ context.Context, project, path string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	bucket, ok := m.data[project]
	if !ok {
		return nil, files.NewError(files.ErrCodeNotFound, "not found: "+path, false)
	}
	body, ok := bucket[path]
	if !ok {
		return nil, files.NewError(files.ErrCodeNotFound, "not found: "+path, false)
	}
	return append([]byte(nil), body...), nil
}

func (m *inMemoryFS) Write(_ context.Context, project, path string, content []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[project]; !ok {
		m.data[project] = map[string][]byte{}
	}
	m.data[project][path] = append([]byte(nil), content...)
	return nil
}

func (m *inMemoryFS) Delete(_ context.Context, project, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if bucket, ok := m.data[project]; ok {
		delete(bucket, path)
	}
	return nil
}

func (m *inMemoryFS) List(_ context.Context, project, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	bucket, ok := m.data[project]
	if !ok {
		return nil, nil
	}
	out := make([]string, 0, len(bucket))
	for k := range bucket {
		out = append(out, k)
	}
	return out, nil
}

func TestSysStoreRoundTrip(t *testing.T) {
	fs := newMemoryFS()
	store := NewSysStore(fs)
	ctx := context.Background()
	tree := &Tree{DocID: "doc1", Type: KindPDF, PageCount: 3, Structure: []*Node{{Title: "T"}}}
	require.NoError(t, store.PutTree(ctx, "p", "doc1", tree))
	require.NoError(t, store.UpdateIndexEntry(ctx, "p", "/a.pdf", IndexEntry{DocID: "doc1", Type: "pdf"}))
	got, err := store.GetTree(ctx, "p", "doc1")
	require.NoError(t, err)
	require.Equal(t, 3, got.PageCount)
	ix, err := store.GetIndex(ctx, "p")
	require.NoError(t, err)
	require.Contains(t, ix, "/a.pdf")
	_, ok, err := store.RemoveIndexEntry(ctx, "p", "/a.pdf")
	require.NoError(t, err)
	require.True(t, ok)
}

// TestSysStoreConcurrentIndexUpdates verifies the store mutex serializes the
// read-modify-write sequence and prevents lost catalog entries.
func TestSysStoreConcurrentIndexUpdates(t *testing.T) {
	fs := newMemoryFS()
	store := NewSysStore(fs)
	ctx := context.Background()
	const n = 10

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			path := "/doc-" + string(rune('a'+i)) + ".pdf"
			docID := "id-" + string(rune('a'+i))
			if err := store.UpdateIndexEntry(ctx, "p", path, IndexEntry{DocID: docID, Type: "pdf"}); err != nil {
				t.Errorf("update %s: %v", path, err)
			}
		}()
	}
	wg.Wait()

	ix, err := store.GetIndex(ctx, "p")
	require.NoError(t, err)
	require.Len(t, ix, n)
}

// TestSysStoreRenameRoundTrip verifies a catalog rename leaves the persisted tree unchanged.
func TestSysStoreRenameRoundTrip(t *testing.T) {
	fs := newMemoryFS()
	store := NewSysStore(fs)
	ctx := context.Background()
	tree := &Tree{DocID: "d1", Type: KindPDF, PageCount: 1, Structure: []*Node{{Title: "T"}}}
	require.NoError(t, store.PutTree(ctx, "p", "d1", tree))
	require.NoError(t, store.UpdateIndexEntry(ctx, "p", "/from.pdf", IndexEntry{DocID: "d1", Type: "pdf"}))
	beforeBytes, err := fs.Read(ctx, "p", treePath("d1"))
	require.NoError(t, err)
	require.NoError(t, store.RenameIndexEntry(ctx, "p", "/from.pdf", "/to.pdf"))
	ix, err := store.GetIndex(ctx, "p")
	require.NoError(t, err)
	require.NotContains(t, ix, "/from.pdf")
	require.Equal(t, "d1", ix["/to.pdf"].DocID)
	afterBytes, err := fs.Read(ctx, "p", treePath("d1"))
	require.NoError(t, err)
	require.Equal(t, string(beforeBytes), string(afterBytes))
}

// TestSysStoreRemoveAndDeleteTree verifies catalog and tree removal complete together.
func TestSysStoreRemoveAndDeleteTree(t *testing.T) {
	fs := newMemoryFS()
	store := NewSysStore(fs)
	ctx := context.Background()
	tree := &Tree{DocID: "d1", Type: KindPDF, PageCount: 1, Structure: []*Node{{Title: "T"}}}
	require.NoError(t, store.PutTree(ctx, "p", "d1", tree))
	require.NoError(t, store.UpdateIndexEntry(ctx, "p", "/p1.pdf", IndexEntry{DocID: "d1", Type: "pdf"}))
	entry, ok, err := store.RemoveIndexEntry(ctx, "p", "/p1.pdf")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, store.DeleteTree(ctx, "p", entry.DocID))
	_, err = fs.Read(ctx, "p", treePath("d1"))
	require.Error(t, err)
	ix, err := store.GetIndex(ctx, "p")
	require.NoError(t, err)
	require.NotContains(t, ix, "/p1.pdf")
}

// TestSysStorePropagatesTransientIndexReadFailure verifies a storage outage is
// not reinterpreted as an empty catalog and cannot overwrite existing state.
func TestSysStorePropagatesTransientIndexReadFailure(t *testing.T) {
	t.Parallel()

	fs := &readFailureSystemFS{}
	store := NewSysStore(fs)
	_, err := store.GetIndex(context.Background(), "project")
	require.ErrorContains(t, err, "transient index read failure")
	err = store.UpdateIndexEntry(context.Background(), "project", "/doc.md", IndexEntry{DocID: "doc"})
	require.ErrorContains(t, err, "transient index read failure")
	require.Zero(t, fs.writes, "a failed catalog read must not be followed by a destructive empty-catalog write")
}

type readFailureSystemFS struct {
	writes int
}

func (*readFailureSystemFS) Read(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("transient index read failure")
}

func (s *readFailureSystemFS) Write(context.Context, string, string, []byte) error {
	s.writes++
	return nil
}

func (*readFailureSystemFS) Delete(context.Context, string, string) error { return nil }

func (*readFailureSystemFS) List(context.Context, string, string) ([]string, error) { return nil, nil }
