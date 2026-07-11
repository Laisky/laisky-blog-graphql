package pageindex

import (
	"context"
	"encoding/json"
	"path"
	"sync"
	"time"

	errors "github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// SysOwner is the system_owner string used by every SystemFS write.
const SysOwner = "pageindex"

// IndexEntry maps a user path to its PageIndex tree.
type IndexEntry struct {
	DocID     string `json:"doc_id"`
	Type      string `json:"type"`
	PageCount int    `json:"page_count,omitempty"`
	LineCount int    `json:"line_count,omitempty"`
	IndexedAt string `json:"indexed_at"`
	// SourceContentHash mirrors Tree.SourceContentHash so search-time candidate
	// selection can reject entries that no longer match the active file (§4.5).
	SourceContentHash string `json:"source_content_hash,omitempty"`
}

// Index is the persisted path↔doc_id catalog (one file per project).
type Index map[string]IndexEntry

// Meta is the per-project descriptor stored at pageindex/_meta.json.
type Meta struct {
	UpdatedAt string `json:"updated_at"`
	Count     int    `json:"count"`
}

// SysStore wraps SystemFS to expose the on-disk layout in §2.6.3.
type SysStore struct {
	sys files.SystemFS
	mu  sync.Mutex
}

// NewSysStore constructs a SysStore from a SystemFS handle.
func NewSysStore(sys files.SystemFS) *SysStore {
	return &SysStore{sys: sys}
}

func treePath(docID string) string { return path.Join("pageindex", docID+".json") }
func indexPath() string            { return path.Join("pageindex", "index.json") }
func metaPath() string             { return path.Join("pageindex", "_meta.json") }

// PutTree persists a tree as JSON.
func (s *SysStore) PutTree(ctx context.Context, project, docID string, tree *Tree) error {
	if tree == nil {
		return errors.New("tree is nil")
	}
	body, err := json.MarshalIndent(tree, "", "  ")
	if err != nil {
		return errors.Wrap(err, "encode tree")
	}
	if err := s.sys.Write(ctx, project, treePath(docID), body); err != nil {
		return errors.Wrap(err, "write tree")
	}
	return nil
}

// GetTree reads a previously-persisted tree.
func (s *SysStore) GetTree(ctx context.Context, project, docID string) (*Tree, error) {
	body, err := s.sys.Read(ctx, project, treePath(docID))
	if err != nil {
		return nil, errors.Wrap(err, "read tree")
	}
	var tree Tree
	if err := json.Unmarshal(body, &tree); err != nil {
		return nil, errors.Wrap(err, "decode tree")
	}
	return &tree, nil
}

// DeleteTree removes the tree JSON for docID. NotFound is silently ignored.
func (s *SysStore) DeleteTree(ctx context.Context, project, docID string) error {
	if err := s.sys.Delete(ctx, project, treePath(docID)); err != nil {
		return errors.Wrap(err, "delete tree")
	}
	return nil
}

// PutIndex writes the path↔doc_id index, mutex-guarded for the project scope.
func (s *SysStore) PutIndex(ctx context.Context, project string, ix Index) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.putIndexLocked(ctx, project, ix)
}

// putIndexLocked is the variant called inside the read-modify-write helpers
// that already hold s.mu. It must never re-lock.
func (s *SysStore) putIndexLocked(ctx context.Context, project string, ix Index) error {
	body, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return errors.Wrap(err, "encode index")
	}
	return s.sys.Write(ctx, project, indexPath(), body)
}

// getIndexLocked mirrors GetIndex but assumes s.mu is held by the caller.
func (s *SysStore) getIndexLocked(ctx context.Context, project string) (Index, error) {
	body, err := s.sys.Read(ctx, project, indexPath())
	if err != nil {
		return Index{}, nil
	}
	if len(body) == 0 {
		return Index{}, nil
	}
	var ix Index
	if err := json.Unmarshal(body, &ix); err != nil {
		return nil, errors.Wrap(err, "decode index")
	}
	if ix == nil {
		ix = Index{}
	}
	return ix, nil
}

// putMetaLocked mirrors PutMeta but assumes s.mu is held by the caller.
func (s *SysStore) putMetaLocked(ctx context.Context, project string, meta Meta) error {
	body, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return errors.Wrap(err, "encode meta")
	}
	return s.sys.Write(ctx, project, metaPath(), body)
}

// GetIndex returns the path↔doc_id catalog for project (empty map if missing).
func (s *SysStore) GetIndex(ctx context.Context, project string) (Index, error) {
	body, err := s.sys.Read(ctx, project, indexPath())
	if err != nil {
		return Index{}, nil
	}
	if len(body) == 0 {
		return Index{}, nil
	}
	var ix Index
	if err := json.Unmarshal(body, &ix); err != nil {
		return nil, errors.Wrap(err, "decode index")
	}
	if ix == nil {
		ix = Index{}
	}
	return ix, nil
}

// PutMeta persists the per-project descriptor.
func (s *SysStore) PutMeta(ctx context.Context, project string, meta Meta) error {
	body, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return errors.Wrap(err, "encode meta")
	}
	return s.sys.Write(ctx, project, metaPath(), body)
}

// GetMeta reads the per-project descriptor (empty value if missing).
func (s *SysStore) GetMeta(ctx context.Context, project string) (Meta, error) {
	body, err := s.sys.Read(ctx, project, metaPath())
	if err != nil {
		return Meta{}, nil
	}
	if len(body) == 0 {
		return Meta{}, nil
	}
	var m Meta
	if err := json.Unmarshal(body, &m); err != nil {
		return Meta{}, errors.Wrap(err, "decode meta")
	}
	return m, nil
}

// UpdateIndexEntry mutates the index for a single path under project lock.
// The full read-modify-write sequence runs while s.mu is held so concurrent
// callers do not lose updates (proposal §5.5.2 P10).
func (s *SysStore) UpdateIndexEntry(ctx context.Context, project, userPath string, entry IndexEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ix, err := s.getIndexLocked(ctx, project)
	if err != nil {
		return err
	}
	ix[userPath] = entry
	if err := s.putIndexLocked(ctx, project, ix); err != nil {
		return err
	}
	return s.putMetaLocked(ctx, project, Meta{UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Count: len(ix)})
}

// RemoveIndexEntry deletes a user-path mapping; missing entries are silently ignored.
func (s *SysStore) RemoveIndexEntry(ctx context.Context, project, userPath string) (IndexEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ix, err := s.getIndexLocked(ctx, project)
	if err != nil {
		return IndexEntry{}, false, err
	}
	entry, ok := ix[userPath]
	if !ok {
		return IndexEntry{}, false, nil
	}
	delete(ix, userPath)
	if err := s.putIndexLocked(ctx, project, ix); err != nil {
		return IndexEntry{}, false, err
	}
	if err := s.putMetaLocked(ctx, project, Meta{UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Count: len(ix)}); err != nil {
		return IndexEntry{}, false, err
	}
	return entry, true, nil
}

// RenameIndexEntry replaces the source path with the destination path.
func (s *SysStore) RenameIndexEntry(ctx context.Context, project, src, dst string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ix, err := s.getIndexLocked(ctx, project)
	if err != nil {
		return err
	}
	entry, ok := ix[src]
	if !ok {
		return nil
	}
	delete(ix, src)
	ix[dst] = entry
	if err := s.putIndexLocked(ctx, project, ix); err != nil {
		return err
	}
	return s.putMetaLocked(ctx, project, Meta{UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Count: len(ix)})
}
