package pageindex

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// pageIndexSummaryInput builds the user-row summary publication. A deterministic
// fallback, or a tree whose LLM work was cut short (degradedReason non-empty), is
// published as degraded with the reason as its safe error code (§4.6).
func pageIndexSummaryInput(summary string, source files.SummarySource, contentHash, degradedReason string) files.PluginSummaryInput {
	status := files.SummaryStatusReady
	if source == files.SummarySourceDeterministicFallback || degradedReason != "" {
		status = files.SummaryStatusDegraded
	}
	return files.PluginSummaryInput{
		ExpectedContentHash: contentHash,
		Summary:             summary,
		WordCount:           files.SummaryWordCount(summary),
		Source:              source,
		Status:              status,
		PromptVersion:       AlgorithmVersion,
		GenerationKey:       contentHash,
		ErrorCode:           degradedReason,
	}
}

// pageIndexWriteState reserves the tree identity in the catalog transaction,
// rather than overwriting a tree derived from the current path.
func pageIndexWriteState(userPath string, tree *Tree, entry IndexEntry, derive func(string) string) files.SystemStateMutator {
	return func(state map[string][]byte) error {
		index, err := pageIndexDecodeState(state)
		if err != nil {
			return err
		}
		old := index[userPath].DocID
		delete(index, userPath)
		docID, err := pageIndexAllocateDocID(userPath, old, derive(userPath), pageIndexReferencedDocIDs(index), derive)
		if err != nil {
			return err
		}
		entry.DocID = docID
		current := *tree
		current.DocID = docID
		treeBody, err := json.MarshalIndent(&current, "", "  ")
		if err != nil {
			return errors.Wrap(err, "encode pageindex tree")
		}
		index[userPath] = entry
		state[treePath(docID)] = treeBody
		pageIndexDropUnreferencedTrees(state, index, []string{old})
		return pageIndexWriteIndexState(state, index)
	}
}

func pageIndexDeleteState(target string, recursive bool) files.SystemStateMutator {
	return func(state map[string][]byte) error {
		index, err := pageIndexDecodeState(state)
		if err != nil {
			return err
		}
		prefix := strings.TrimSuffix(target, "/") + "/"
		var removed []string
		for userPath, entry := range index {
			if userPath != target && (!recursive || !strings.HasPrefix(userPath, prefix)) {
				continue
			}
			removed = append(removed, entry.DocID)
			delete(index, userPath)
		}
		pageIndexDropUnreferencedTrees(state, index, removed)
		return pageIndexWriteIndexState(state, index)
	}
}

// pageIndexMove is one catalog entry moved by a rename.
type pageIndexMove struct {
	from string
	to   string
	row  IndexEntry
}

// pageIndexRenameMoves returns the catalog entries at src or under it, mapped
// onto dst the same way the user-file rename maps their paths.
func pageIndexRenameMoves(index Index, src, dst string) []pageIndexMove {
	prefix := strings.TrimSuffix(src, "/") + "/"
	moves := make([]pageIndexMove, 0)
	for userPath, entry := range index {
		if userPath == src {
			moves = append(moves, pageIndexMove{from: userPath, to: dst, row: entry})
			continue
		}
		if strings.HasPrefix(userPath, prefix) {
			moves = append(moves, pageIndexMove{from: userPath, to: dst + strings.TrimPrefix(userPath, src), row: entry})
		}
	}
	return moves
}

// pageIndexOverwritten reports whether userPath is replaced by a move destination.
func pageIndexOverwritten(userPath string, moves []pageIndexMove) bool {
	for _, candidate := range moves {
		if userPath == candidate.to || strings.HasPrefix(userPath, strings.TrimSuffix(candidate.to, "/")+"/") {
			return true
		}
	}
	return false
}

func pageIndexRenameState(src, dst string, overwrite bool) files.SystemStateMutator {
	return func(state map[string][]byte) error {
		index, err := pageIndexDecodeState(state)
		if err != nil {
			return err
		}
		moves := pageIndexRenameMoves(index, src, dst)
		var removed []string
		if overwrite {
			for userPath, entry := range index {
				if pageIndexOverwritten(userPath, moves) {
					removed = append(removed, entry.DocID)
					delete(index, userPath)
				}
			}
		}
		for _, item := range moves {
			delete(index, item.from)
		}
		for _, item := range moves {
			index[item.to] = item.row
		}
		pageIndexDropUnreferencedTrees(state, index, removed)
		return pageIndexWriteIndexState(state, index)
	}
}

// pageIndexMoveDocIDAttempts bounds the search for a free target doc_id.
const pageIndexMoveDocIDAttempts = 16

// pageIndexMovePlan reads both catalogs but only source and displaced tree
// bodies for the selected FileIO paths.
func pageIndexMovePlan(derive func(string) string) files.SystemStateMovePlan {
	return files.SystemStateMovePlan{
		CatalogPaths:  []string{indexPath(), metaPath()},
		AffectedPaths: pageIndexMoveTreePaths,
		Mutate:        pageIndexMappedMoveState(derive),
	}
}

// pageIndexMoveTreePaths selects each affected tree at most once.
func pageIndexMoveTreePaths(change files.RenameStateChange, source, target map[string][]byte) ([]string, []string, error) {
	sourceIndex, err := pageIndexDecodeState(source)
	if err != nil {
		return nil, nil, err
	}
	targetIndex := sourceIndex
	if target != nil {
		targetIndex, err = pageIndexDecodeState(target)
		if err != nil {
			return nil, nil, err
		}
	}
	sourceSet, targetSet := map[string]struct{}{}, map[string]struct{}{}
	if target == nil {
		targetSet = sourceSet
	}
	add := func(set map[string]struct{}, id string) {
		if id != "" {
			set[treePath(id)] = struct{}{}
		}
	}
	for _, mapping := range change.Mappings {
		add(sourceSet, sourceIndex[mapping.OldPath].DocID)
		add(targetSet, targetIndex[mapping.NewPath].DocID)
	}
	for _, overwritten := range change.OverwrittenPaths {
		add(targetSet, targetIndex[overwritten].DocID)
	}
	var targetPaths []string
	if target != nil {
		targetPaths = slices.Collect(maps.Keys(targetSet))
	}
	return slices.Collect(maps.Keys(sourceSet)), targetPaths, nil
}

// pageIndexMappedMoveState applies only file changes selected by FileIO.
// Documents without a usable tree are refused before either namespace commits.
func pageIndexMappedMoveState(derive func(string) string) files.SystemStateMoveMutator {
	return func(change files.RenameStateChange, source, target map[string][]byte) error {
		sourceIndex, err := pageIndexDecodeState(source)
		if err != nil {
			return err
		}
		cross := target != nil
		targetIndex := sourceIndex
		if cross {
			targetIndex, err = pageIndexDecodeState(target)
			if err != nil {
				return err
			}
		} else {
			target = source
		}
		type treeMove struct {
			entry IndexEntry
			tree  Tree
			path  string
		}
		var incoming []treeMove
		var sourceRemoved, targetRemoved []string
		for _, mapping := range change.Mappings {
			entry := sourceIndex[mapping.OldPath]
			delete(sourceIndex, mapping.OldPath)
			if entry.DocID != "" {
				sourceRemoved = append(sourceRemoved, entry.DocID)
			}
			if !isLongDocPath(mapping.NewPath) {
				continue
			}
			if !isLongDocPath(mapping.OldPath) || pageIndexDocKind(mapping.OldPath) != pageIndexDocKind(mapping.NewPath) {
				return errors.WithStack(files.NewError(files.ErrCodeInvalidArgument,
					"destination changes the document format; write the document with the new extension instead", false))
			}
			var tree Tree
			body := source[treePath(entry.DocID)]
			if !mapping.SkipRAGIndex || mapping.ContentHash == "" || entry.SourceContentHash != mapping.ContentHash ||
				len(body) == 0 || json.Unmarshal(body, &tree) != nil || tree.DocID != entry.DocID ||
				tree.SourceContentHash != mapping.ContentHash || tree.Type != pageIndexDocKind(mapping.NewPath) {
				return errors.WithStack(files.NewError(files.ErrCodeResourceBusy,
					"PageIndex tree is not current; retry after indexing completes", true))
			}
			incoming = append(incoming, treeMove{entry: entry, tree: tree, path: mapping.NewPath})
		}
		// A catalog entry at a destination without a live file is stale too.
		for _, mapping := range change.Mappings {
			if old, ok := targetIndex[mapping.NewPath]; ok {
				targetRemoved = append(targetRemoved, old.DocID)
				delete(targetIndex, mapping.NewPath)
			}
		}
		for _, overwritten := range change.OverwrittenPaths {
			if old, ok := targetIndex[overwritten]; ok {
				targetRemoved = append(targetRemoved, old.DocID)
				delete(targetIndex, overwritten)
			}
		}
		if len(sourceRemoved) == 0 && len(targetRemoved) == 0 && len(incoming) == 0 {
			return nil
		}
		used := pageIndexReferencedDocIDs(targetIndex)
		for _, item := range incoming {
			preferred, fallback := item.entry.DocID, derive(item.path)
			if cross {
				preferred, fallback = fallback, preferred
			}
			docID, err := pageIndexAllocateDocID(item.path, preferred, fallback, used, derive)
			if err != nil {
				return err
			}
			item.entry.DocID, item.tree.DocID = docID, docID
			body, err := json.MarshalIndent(&item.tree, "", "  ")
			if err != nil {
				return errors.Wrap(err, "encode moved pageindex tree")
			}
			used[docID] = struct{}{}
			targetIndex[item.path] = item.entry
			target[treePath(docID)] = body
		}
		pageIndexDropUnreferencedTrees(source, sourceIndex, sourceRemoved)
		pageIndexDropUnreferencedTrees(target, targetIndex, targetRemoved)
		if cross {
			if err := pageIndexWriteIndexState(source, sourceIndex); err != nil {
				return err
			}
		}
		return pageIndexWriteIndexState(target, targetIndex)
	}
}

// pageIndexAllocateDocID picks an identity no other catalog entry references.
func pageIndexAllocateDocID(userPath, preferred, fallback string, used map[string]struct{}, derive func(string) string) (string, error) {
	for _, candidate := range []string{preferred, fallback} {
		if _, taken := used[candidate]; candidate != "" && !taken {
			return candidate, nil
		}
	}
	for attempt := 1; attempt < pageIndexMoveDocIDAttempts; attempt++ {
		candidate := derive(userPath + "\x00" + strconv.Itoa(attempt))
		if _, taken := used[candidate]; !taken {
			return candidate, nil
		}
	}
	return "", errors.Errorf("no free pageindex doc_id for %s", userPath)
}

// pageIndexReferencedDocIDs returns the doc_ids referenced by index entries.
func pageIndexReferencedDocIDs(index Index) map[string]struct{} {
	used := make(map[string]struct{}, len(index))
	for _, entry := range index {
		used[entry.DocID] = struct{}{}
	}
	return used
}

// pageIndexDropUnreferencedTrees deletes the trees of docIDs that no remaining
// entry of index still references.
func pageIndexDropUnreferencedTrees(state map[string][]byte, index Index, docIDs []string) {
	referenced := pageIndexReferencedDocIDs(index)
	for _, docID := range docIDs {
		if _, keep := referenced[docID]; !keep {
			delete(state, treePath(docID))
		}
	}
}

func pageIndexDecodeState(state map[string][]byte) (Index, error) {
	body := state[indexPath()]
	if len(body) == 0 {
		return Index{}, nil
	}
	var index Index
	if err := json.Unmarshal(body, &index); err != nil {
		return nil, errors.Wrap(err, "decode pageindex index")
	}
	if index == nil {
		index = Index{}
	}
	return index, nil
}

func pageIndexWriteIndexState(state map[string][]byte, index Index) error {
	indexBody, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return errors.Wrap(err, "encode pageindex index")
	}
	metaBody, err := json.MarshalIndent(Meta{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Count:     len(index),
	}, "", "  ")
	if err != nil {
		return errors.Wrap(err, "encode pageindex metadata")
	}
	state[indexPath()] = indexBody
	state[metaPath()] = metaBody
	return nil
}
