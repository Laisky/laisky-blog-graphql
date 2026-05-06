package pageindex

import (
	"context"
	"sync"
	"testing"
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
		return nil, &notFoundError{path: path}
	}
	body, ok := bucket[path]
	if !ok {
		return nil, &notFoundError{path: path}
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

type notFoundError struct{ path string }

func (e *notFoundError) Error() string { return "not found: " + e.path }

func TestSysStoreRoundTrip(t *testing.T) {
	fs := newMemoryFS()
	store := NewSysStore(fs)
	ctx := context.Background()
	tree := &Tree{DocID: "doc1", Type: KindPDF, PageCount: 3, Structure: []*Node{{Title: "T"}}}
	if err := store.PutTree(ctx, "p", "doc1", tree); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateIndexEntry(ctx, "p", "/a.pdf", IndexEntry{DocID: "doc1", Type: "pdf"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTree(ctx, "p", "doc1")
	if err != nil {
		t.Fatal(err)
	}
	if got.PageCount != 3 {
		t.Fatalf("page count: %d", got.PageCount)
	}
	ix, err := store.GetIndex(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ix["/a.pdf"]; !ok {
		t.Fatalf("missing index entry: %+v", ix)
	}
	if _, ok, _ := store.RemoveIndexEntry(ctx, "p", "/a.pdf"); !ok {
		t.Fatal("expected entry to be removed")
	}
}

// TestSysStoreConcurrentIndexUpdates (P10) — ten goroutines each call
// UpdateIndexEntry against distinct user paths in the same project. The
// SysStore mutex must serialize the read-modify-write so the final mapping
// holds all ten entries with no lost updates.
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
	if err != nil {
		t.Fatal(err)
	}
	if len(ix) != n {
		t.Fatalf("expected %d entries after concurrent updates, got %d (lost updates indicate missing serialization)", n, len(ix))
	}
}

// TestSysStoreRenameRoundTrip (P09 — sysstore portion) — verifies that
// RenameIndexEntry moves the mapping from src to dst while leaving the
// underlying tree JSON in SystemFS untouched.
func TestSysStoreRenameRoundTrip(t *testing.T) {
	fs := newMemoryFS()
	store := NewSysStore(fs)
	ctx := context.Background()
	tree := &Tree{DocID: "d1", Type: KindPDF, PageCount: 1, Structure: []*Node{{Title: "T"}}}
	if err := store.PutTree(ctx, "p", "d1", tree); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateIndexEntry(ctx, "p", "/from.pdf", IndexEntry{DocID: "d1", Type: "pdf"}); err != nil {
		t.Fatal(err)
	}
	// Capture the on-disk tree bytes so we can confirm rename does not touch them.
	beforeBytes, err := fs.Read(ctx, "p", treePath("d1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RenameIndexEntry(ctx, "p", "/from.pdf", "/to.pdf"); err != nil {
		t.Fatal(err)
	}
	ix, err := store.GetIndex(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := ix["/from.pdf"]; exists {
		t.Errorf("rename: src path still in index: %+v", ix)
	}
	if entry, exists := ix["/to.pdf"]; !exists || entry.DocID != "d1" {
		t.Errorf("rename: dst path missing or wrong doc_id: %+v", ix)
	}
	afterBytes, err := fs.Read(ctx, "p", treePath("d1"))
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeBytes) != string(afterBytes) {
		t.Error("rename must not mutate persisted tree JSON")
	}
}

// TestSysStoreRemoveAndDeleteTree (P08 — sysstore portion) — RemoveIndexEntry
// returns the entry so the caller can DeleteTree, after which both the mapping
// and the tree JSON are absent from the SystemFS.
func TestSysStoreRemoveAndDeleteTree(t *testing.T) {
	fs := newMemoryFS()
	store := NewSysStore(fs)
	ctx := context.Background()
	tree := &Tree{DocID: "d1", Type: KindPDF, PageCount: 1, Structure: []*Node{{Title: "T"}}}
	if err := store.PutTree(ctx, "p", "d1", tree); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateIndexEntry(ctx, "p", "/p1.pdf", IndexEntry{DocID: "d1", Type: "pdf"}); err != nil {
		t.Fatal(err)
	}
	entry, ok, err := store.RemoveIndexEntry(ctx, "p", "/p1.pdf")
	if err != nil || !ok {
		t.Fatalf("remove entry ok=%v err=%v", ok, err)
	}
	if err := store.DeleteTree(ctx, "p", entry.DocID); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Read(ctx, "p", treePath("d1")); err == nil {
		t.Error("delete-tree: tree JSON should be absent from SystemFS")
	}
	ix, err := store.GetIndex(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := ix["/p1.pdf"]; exists {
		t.Errorf("delete: index still contains removed path: %+v", ix)
	}
}
