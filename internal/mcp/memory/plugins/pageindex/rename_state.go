package pageindex

import (
	"context"
	"encoding/json"

	glog "github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// renameCatalog is one tenant namespace's catalog, loaded inside a rename
// transaction. A nil index stands for a catalog that could not be decoded: it
// is left untouched, and search cannot use it either.
type renameCatalog struct {
	plugin  *Plugin
	project string
	index   Index
	trees   map[string][]byte // tree bodies to store, by doc_id
	dropped []string          // doc_ids of removed entries
	changed bool
}

// loadRenameCatalog reads the catalog of a storage project. Only a storage
// error fails; a malformed catalog is logged and returned without an index.
func (p *Plugin) loadRenameCatalog(ctx context.Context, state files.SystemStateTx, project string) (*renameCatalog, error) {
	body, err := state.Read(ctx, project, indexPath())
	if err != nil {
		return nil, err
	}
	index, err := pageIndexDecodeState(body)
	if err != nil {
		if p.log != nil {
			p.log.Warn("pageindex.rename leaves an unreadable catalog untouched", glog.String("storage_project", project), glog.Error(err))
		}
		return &renameCatalog{plugin: p, project: project}, nil
	}
	return &renameCatalog{plugin: p, project: project, index: index, trees: map[string][]byte{}}, nil
}

// remove drops the entry of userPath, if any, and returns it.
func (c *renameCatalog) remove(userPath string) (IndexEntry, bool) {
	if c.index == nil {
		return IndexEntry{}, false
	}
	entry, ok := c.index[userPath]
	if !ok {
		return IndexEntry{}, false
	}
	delete(c.index, userPath)
	if entry.DocID != "" {
		c.dropped = append(c.dropped, entry.DocID)
	}
	c.changed = true
	return entry, true
}

// persist stores the new trees and the catalog, then deletes the trees that no
// remaining entry references. A catalog that would exceed the file size limit
// is left untouched rather than failing the rename.
func (c *renameCatalog) persist(ctx context.Context, state files.SystemStateTx) error {
	if c.index == nil || !c.changed {
		return nil
	}
	indexBody, metaBody, err := pageIndexEncodeIndex(c.index)
	if err != nil {
		return err
	}
	if !state.Fits(indexBody) {
		if c.plugin.log != nil {
			c.plugin.log.Warn("pageindex.rename leaves a full catalog untouched", glog.String("storage_project", c.project), glog.Int("bytes", len(indexBody)))
		}
		return nil
	}
	for docID, body := range c.trees {
		if err := state.Write(ctx, c.project, treePath(docID), body); err != nil {
			return err
		}
	}
	referenced := pageIndexReferencedDocIDs(c.index)
	for _, docID := range c.dropped {
		if _, keep := referenced[docID]; keep {
			continue
		}
		if err := state.Delete(ctx, c.project, treePath(docID)); err != nil {
			return err
		}
	}
	if err := state.Write(ctx, c.project, indexPath(), indexBody); err != nil {
		return err
	}
	return state.Write(ctx, c.project, metaPath(), metaBody)
}

// renameState returns the hook that keeps PageIndex state consistent with one
// FileIO rename, inside its transaction. The file move is authoritative. A tree
// moves with its file only when it verifiably describes the moved bytes and the
// destination keeps the document kind; it is then re-keyed to the doc_id of
// the destination path. Every other entry of a moved or displaced path is
// dropped, so search never returns the old path or stale content, and the file
// is indexed again by its next write. The hook never calls the model.
func (p *Plugin) renameState(auth files.AuthContext, req files.RenameRequest) files.RenameStateHook {
	targetProject := req.Destination()
	return func(ctx context.Context, change files.RenameStateChange, state files.SystemStateTx) error {
		source, err := p.loadRenameCatalog(ctx, state, PageIndexStorageProject(auth, req.Project))
		if err != nil {
			return err
		}
		target := source
		if targetProject != req.Project {
			if target, err = p.loadRenameCatalog(ctx, state, PageIndexStorageProject(auth, targetProject)); err != nil {
				return err
			}
		}

		// Take every source entry before clearing destinations: within one
		// namespace a new path can be another moved file's old path.
		var carried []renameCarry
		for _, mapping := range change.Mappings {
			entry, indexed := source.remove(mapping.OldPath)
			if indexed && target.index != nil && pageIndexReusable(mapping, entry) {
				carried = append(carried, renameCarry{mapping: mapping, entry: entry})
			}
		}
		for _, mapping := range change.Mappings {
			target.remove(mapping.NewPath)
		}
		for _, overwritten := range change.OverwrittenPaths {
			target.remove(overwritten)
		}
		if err := carryTrees(ctx, auth, targetProject, state, source, target, carried); err != nil {
			return err
		}

		if err := source.persist(ctx, state); err != nil {
			return err
		}
		if target != source {
			return target.persist(ctx, state)
		}
		return nil
	}
}

// renameCarry is a moved file whose catalog entry matches the moved bytes.
type renameCarry struct {
	mapping files.RenameMapping
	entry   IndexEntry
}

// carryTrees re-keys the current trees of the carried files into target, under
// the doc_id of the destination path (see pageIndexClaimDocID). A tree that is
// missing, malformed, describes other bytes or exceeds the size limit is not
// carried.
func carryTrees(ctx context.Context, auth files.AuthContext, targetProject string, state files.SystemStateTx,
	source, target *renameCatalog, carried []renameCarry,
) error {
	if len(carried) == 0 {
		return nil
	}
	paths := make([]string, 0, len(carried))
	for _, item := range carried {
		paths = append(paths, treePath(item.entry.DocID))
	}
	bodies, err := state.Read(ctx, source.project, paths...)
	if err != nil {
		return err
	}
	for _, item := range carried {
		docID := docIDFromAuth(auth, targetProject, item.mapping.NewPath)
		body, ok := pageIndexRekeyTree(bodies[treePath(item.entry.DocID)], item.mapping, docID)
		if !ok || !state.Fits(body) {
			continue
		}
		entry := item.entry
		entry.DocID = docID
		pageIndexClaimDocID(target.index, item.mapping.NewPath, docID)
		target.index[item.mapping.NewPath] = entry
		target.trees[docID] = body
		target.changed = true
	}
	return nil
}

// pageIndexReusable reports whether entry may still describe the moved file at
// its destination: both paths have the same indexable kind and the entry was
// built from exactly the bytes being moved.
func pageIndexReusable(mapping files.RenameMapping, entry IndexEntry) bool {
	return isLongDocPath(mapping.OldPath) && isLongDocPath(mapping.NewPath) &&
		pageIndexDocKind(mapping.OldPath) == pageIndexDocKind(mapping.NewPath) &&
		mapping.ContentHash != "" && entry.SourceContentHash == mapping.ContentHash
}

// pageIndexRekeyTree returns the tree body stored under docID, or false when
// the body is missing, malformed, or describes other bytes or another kind.
func pageIndexRekeyTree(body []byte, mapping files.RenameMapping, docID string) ([]byte, bool) {
	var tree Tree
	if len(body) == 0 || json.Unmarshal(body, &tree) != nil ||
		tree.SourceContentHash != mapping.ContentHash || tree.Type != pageIndexDocKind(mapping.NewPath) {
		return nil, false
	}
	tree.DocID = docID
	out, err := json.MarshalIndent(&tree, "", "  ")
	if err != nil {
		return nil, false
	}
	return out, true
}
